package main

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

const dir = "/var/lib/powerdns/isupipe"
const path = dir + "/u.isucon.dev.zone"

func update() error {
	args := os.Args[1:]
	if len(args) < 2 {
		return fmt.Errorf("missing arguments")
	}
	ip := net.ParseIP(args[len(args)-1])
	if ip.To4() == nil {
		return fmt.Errorf("invalid IPv4 address")
	}
	lock, err := os.OpenFile(dir+"/zone.lock", os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	var data []byte
	switch args[0] {
	case "init":
		data, err = os.ReadFile("u.isucon.dev.zone")
		if err != nil {
			return err
		}
		data = bytes.ReplaceAll(data, []byte("<ISUCON_SUBDOMAIN_ADDRESS>"), []byte(ip.String()))
		data = bytes.ReplaceAll(data, []byte("0 IN A"), []byte("60 IN A"))
	case "add":
		if len(args) != 3 || len(args[1])+len(".u.isucon.dev") > 253 {
			return fmt.Errorf("invalid domain")
		}
		var name strings.Builder
		for _, label := range strings.Split(args[1], ".") {
			if len(label) == 0 || len(label) > 63 {
				return fmt.Errorf("invalid label")
			}
			for i := 0; i < len(label); i++ {
				c := label[i]
				if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' {
					name.WriteByte(c)
				} else {
					fmt.Fprintf(&name, "\\%03d", c)
				}
			}
			name.WriteByte('.')
		}
		data, err = os.ReadFile(path)
		if err != nil {
			return err
		}
		data = fmt.Appendf(data, "\n%su.isucon.dev. 60 IN A %s\n", name.String(), ip.String())
	default:
		return fmt.Errorf("unknown operation")
	}
	f, err := os.CreateTemp(dir, "zone-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Chmod(0644); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err = directory.Sync(); err != nil {
		return err
	}
	out, err := exec.Command("pdns_control", "bind-reload-now", "u.isucon.dev.").CombinedOutput()
	if err != nil || !bytes.Contains(out, []byte("parsed into memory")) {
		return fmt.Errorf("reloading zone: %s (%v)", out, err)
	}
	return nil
}
func main() {
	if err := update(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
