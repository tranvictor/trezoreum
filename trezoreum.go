package trezoreum

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/golang/protobuf/proto"
	mc "github.com/tranvictor/trezoreum/messages_common"
	me "github.com/tranvictor/trezoreum/messages_ethereum"
	mm "github.com/tranvictor/trezoreum/messages_management"
	"github.com/trezor/trezord-go/core"
	"github.com/trezor/trezord-go/memorywriter"
	"github.com/trezor/trezord-go/usb"
)

type Trezoreum struct {
	session string
	core    *core.Core
}

func NewTrezoreum() (*Trezoreum, error) {
	longMemoryWriter := memorywriter.New(90000, 200, true, false)
	bus, err := initUsb(longMemoryWriter)
	if err != nil {
		return nil, err
	}
	b := usb.Init(bus...)
	c := core.New(b, longMemoryWriter, allowCancel())
	return &Trezoreum{
		core: c,
	}, nil
}

// trezorExchange performs a data exchange with the Trezor wallet, sending it a
// message and retrieving the response. If multiple responses are possible, the
// method will also return the index of the destination object used.
func (self *Trezoreum) trezorExchange(req proto.Message, results ...proto.Message) (int, error) {
	// Construct the original message payload to chunk up
	data, err := ToTrezorPackage(req)
	if err != nil {
		return 0, err
	}

	reply, err := self.core.Call(data, self.session, core.CallModeReadWrite, false, make(chan bool))
	if err != nil {
		return 0, err
	}

	kind := binary.BigEndian.Uint16(reply[0:2])

	// Try to parse the reply into the requested reply message
	if kind == uint16(MessageType_MessageType_Failure) {
		// Trezor returned a failure, extract and return the message
		failure := new(mc.Failure)
		if err := proto.Unmarshal(reply[6:], failure); err != nil {
			return 0, err
		}
		return 0, errors.New("trezor: " + failure.GetMessage())
	}
	if kind == uint16(MessageType_MessageType_ButtonRequest) {
		// Trezor is waiting for user confirmation, ack and wait for the next message
		return self.trezorExchange(&mc.ButtonAck{}, results...)
	}
	for i, res := range results {
		if Type(res) == kind {
			return i, proto.Unmarshal(reply[6:], res)
		}
	}
	expected := make([]string, len(results))
	for i, res := range results {
		expected[i] = Name(Type(res))
	}
	return 0, fmt.Errorf("trezor: expected reply types %s, got %s", expected, Name(kind))
}

func (self *Trezoreum) Init() (mm.Features, TrezorState, error) {
	devices, err := self.core.Enumerate()
	if err != nil {
		return mm.Features{}, Unexpected, err
	}
	if len(devices) == 0 {
		return mm.Features{}, Unexpected, fmt.Errorf("Couldn't find any trezor devices")
	}

	// assume we only have valid device
	device := devices[0]
	session := device.Session
	if session == nil {
		self.session = ""
	} else {
		self.session = *session
	}
	s, err := self.core.Acquire(device.Path, self.session, false)
	if err != nil {
		return mm.Features{}, Unexpected, err
	}
	self.session = s

	// test init device
	initMsg := mm.Initialize{}
	features := mm.Features{}

	_, err = self.trezorExchange(&initMsg, &features)
	if err != nil {
		return mm.Features{}, Unexpected, err
	}

	// Do a manual ping, forcing the device to ask for its PIN and Passphrase
	askPin := true
	askPassphrase := true

	res, err := self.trezorExchange(&mm.Ping{PinProtection: &askPin, PassphraseProtection: &askPassphrase}, new(mc.PinMatrixRequest), new(mc.PassphraseRequest), new(mc.Success))
	if err != nil {
		return mm.Features{}, Unexpected, err
	}

	switch res {
	case 0:
		return features, WaitingForPin, nil
	case 1:
		return features, WaitingForPassphrase, nil
	case 2:
		return features, Ready, nil
	default:
		return features, Ready, nil
	}
}

func (self *Trezoreum) UnlockByPin(pin string) (TrezorState, error) {
	res, err := self.trezorExchange(&mc.PinMatrixAck{Pin: &pin}, new(mc.Success), new(mc.PassphraseRequest))
	if err != nil {
		return Unexpected, err
	}
	if res == 1 {
		return WaitingForPassphrase, nil
	}
	return Ready, nil
}

func (self *Trezoreum) UnlockByPassphrase(passphrase string) (TrezorState, error) {
	return Unexpected, fmt.Errorf("Not implemented")
}

func (self *Trezoreum) Derive(path accounts.DerivationPath) (common.Address, error) {
	address := me.EthereumAddress{}
	if _, err := self.trezorExchange(&me.EthereumGetAddress{AddressN: []uint32(path)}, &address); err != nil {
		return common.Address{}, err
	}
	// we have to use XXX_unrecognized here because the proto file we are using
	// is different to the one that was used in firmware 1.7.3
	return common.BytesToAddress(address.GetAddress()), nil
}

func (self *Trezoreum) Sign(path accounts.DerivationPath, tx *types.Transaction, chainID *big.Int) (common.Address, *types.Transaction, error) {
	// Create the transaction initiation message
	data := tx.Data()
	length := uint32(len(data))

	request := &me.EthereumSignTx{
		AddressN:   path,
		Nonce:      new(big.Int).SetUint64(tx.Nonce()).Bytes(),
		GasPrice:   tx.GasPrice().Bytes(),
		GasLimit:   new(big.Int).SetUint64(tx.Gas()).Bytes(),
		Value:      tx.Value().Bytes(),
		DataLength: &length,
	}
	if to := tx.To(); to != nil {
		request.To = (*to)[:] // Non contract deploy, set recipient explicitly
	}
	if length > 1024 { // Send the data chunked if that was requested
		request.DataInitialChunk, data = data[:1024], data[1024:]
	} else {
		request.DataInitialChunk, data = data, nil
	}
	if chainID != nil { // EIP-155 transaction, set chain ID explicitly (only 32 bit is supported!?)
		id := uint32(chainID.Int64())
		request.ChainId = &id
	}
	// Send the initiation message and stream content until a signature is returned
	response := new(me.EthereumTxRequest)
	if _, err := self.trezorExchange(request, response); err != nil {
		return common.Address{}, nil, err
	}
	for response.DataLength != nil && int(*response.DataLength) <= len(data) {
		chunk := data[:*response.DataLength]
		data = data[*response.DataLength:]

		if _, err := self.trezorExchange(&me.EthereumTxAck{DataChunk: chunk}, response); err != nil {
			return common.Address{}, nil, err
		}
	}
	// Extract the Ethereum signature and do a sanity validation
	if len(response.GetSignatureR()) == 0 || len(response.GetSignatureS()) == 0 || response.GetSignatureV() == 0 {
		return common.Address{}, nil, errors.New("reply lacks signature")
	}
	signature := append(append(response.GetSignatureR(), response.GetSignatureS()...), byte(response.GetSignatureV()))

	// Create the correct signer and signature transform based on the chain ID
	var signer types.Signer
	if chainID == nil {
		signer = new(types.HomesteadSigner)
	} else {
		signer = types.NewEIP155Signer(chainID)
		signature[64] -= byte(chainID.Uint64()*2 + 35)
	}
	// Inject the final signature into the transaction and sanity check the sender
	signed, err := tx.WithSignature(signer, signature)
	if err != nil {
		return common.Address{}, nil, err
	}
	sender, err := types.Sender(signer, signed)
	if err != nil {
		return common.Address{}, nil, err
	}
	return sender, signed, nil
}
