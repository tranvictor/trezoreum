package trezoreum

import (
	"runtime"

	"github.com/trezor/trezord-go/core"
	"github.com/trezor/trezord-go/memorywriter"
	"github.com/trezor/trezord-go/usb"
)

// Does OS allow sync canceling via our custom libusb patches?
func allowCancel() bool {
	return runtime.GOOS != "freebsd"
}

// Does OS detach kernel driver in libusb?
func detachKernelDriver() bool {
	return runtime.GOOS == "linux"
}

// Does OS use libusb for HID devices?
func useOnlyLibusb() bool {
	return runtime.GOOS == "freebsd" || runtime.GOOS == "linux"
}

func initUsb(wr *memorywriter.MemoryWriter) ([]core.USBBus, error) {
	w, err := usb.InitLibUSB(wr, useOnlyLibusb(), allowCancel(), detachKernelDriver())
	if err != nil {
		return nil, err
	}

	if useOnlyLibusb() {
		return []core.USBBus{w}, nil
	}

	h, err := usb.InitHIDAPI(wr)
	if err != nil {
		return nil, err
	}
	return []core.USBBus{w, h}, nil
}
