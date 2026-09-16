//go:build darwin || freebsd || netbsd || openbsd

package main

import "syscall"

const ioctlReadTermios = syscall.TIOCGETA
