package main

import (
	"fmt"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/tranvictor/trezoreum"
)

func main() {
	trezor, err := trezoreum.NewTrezoreum()
	if err != nil {
		fmt.Printf("Init trezor failed: %s\n", err)
		return
	}

	info, state, err := trezor.Init()
	if err != nil {
		fmt.Printf("Init connection to trezor failed: %s\n", err)
		return
	}
	fmt.Printf("Firmware version: %d.%d.%d\n", *info.MajorVersion, *info.MinorVersion, *info.PatchVersion)
	for state != trezoreum.Ready {
		if state == trezoreum.WaitingForPin {
			pin := trezoreum.PromptPINFromStdin()
			state, err = trezor.UnlockByPin(pin)
			if err != nil {
				fmt.Printf("Pin error: %s\n", err)
			}
		} else if state == trezoreum.WaitingForPassphrase {
			fmt.Printf("Not support passphrase yet\n")
		}
	}
	fmt.Printf("Trezor is unlocked, please do more\n")
	path := "m/44'/60'/0'/0/2"
	p, err := accounts.ParseDerivationPath(path)
	if err != nil {
		fmt.Printf("Parsing derivation path failed: %s\n", err)
		return
	}
	addr, err := trezor.Derive(p)
	if err != nil {
		fmt.Printf("Derive address failed: %s\n", err)
	} else {
		fmt.Printf("Got address: %s\n", addr.Hex())
	}
}
