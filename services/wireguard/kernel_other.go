//go:build !linux

package main

import (
	"errors"
	"net/netip"

	"linxpbx.com/linx/internal/wgconf"
)

// kernel: linx-wireguard only runs on Linux.
type kernel struct{}

func newKernel() (*kernel, error) { return nil, errors.New("linx-wireguard runs on Linux only") }

func (*kernel) Close() error                                  { return nil }
func (*kernel) Links() ([]Link, error)                        { return nil, errors.ErrUnsupported }
func (*kernel) ApplyTunnel(wgconf.Tunnel, []netip.Addr) error { return errors.ErrUnsupported }
func (*kernel) RemoveTunnel(string) error                     { return errors.ErrUnsupported }
func (*kernel) EnsureRule() error                             { return errors.ErrUnsupported }
func (*kernel) Device(string) (Device, error)                 { return Device{}, errors.ErrUnsupported }
