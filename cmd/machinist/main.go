//go:build darwin || linux

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mitkox/esf/internal/cli"
)

var version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	sigpipe := make(chan os.Signal, 1)
	signal.Notify(sigpipe, syscall.SIGPIPE)
	defer signal.Stop(sigpipe)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stdout, closeStdout, err := interruptibleOutput(os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "machinist: prepare stdout: %s\n", err)
		return 1
	}
	defer closeStdout()
	stderr, closeStderr, err := interruptibleOutput(os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "machinist: prepare stderr: %s\n", err)
		return 1
	}
	defer closeStderr()

	return cli.Execute(ctx, os.Args[1:], os.Stdin, stdout, stderr, version)
}

func interruptibleOutput(file *os.File) (*os.File, func(), error) {
	info, err := file.Stat()
	if err != nil {
		return nil, func() {}, err
	}
	if info.Mode()&(os.ModeNamedPipe|os.ModeSocket) == 0 {
		return file, func() {}, nil
	}
	descriptor, err := syscall.Dup(int(file.Fd()))
	if err != nil {
		return nil, func() {}, err
	}
	syscall.CloseOnExec(descriptor)
	if err := syscall.SetNonblock(descriptor, true); err != nil {
		_ = syscall.Close(descriptor)
		return nil, func() {}, err
	}
	duplicate := os.NewFile(uintptr(descriptor), file.Name())
	if duplicate == nil {
		_ = syscall.Close(descriptor)
		return nil, func() {}, fmt.Errorf("wrap duplicated descriptor")
	}
	return duplicate, func() { _ = duplicate.Close() }, nil
}
