package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

func _progress(a []interface{}, withEol bool) {
	repeatLen := 15 - len(hostname)
	if repeatLen <= 0 {
		repeatLen = 1
	}
	now := time.Now()

	msg := "\r\033[2K"
	msg += fmt.Sprintf("%s.%09d ", now.Format("15:04:05"), now.Nanosecond())
	msg += fmt.Sprint(" ", hostname, "$ ", strings.Repeat(" ", repeatLen))
	msg += fmt.Sprint(a...)
	if withEol {
		msg += fmt.Sprint("\n")
	}
	fmt.Fprint(os.Stderr, msg)
}

func progress(a ...interface{}) {
	_progress(a, false)
}

func progressLn(a ...interface{}) {
	_progress(a, true)
}

func fatalLn(a ...interface{}) {
	progressLn(a...)
	os.Exit(1)
}

func debugLn(a ...interface{}) {
	if isDebug {
		progressLn(a...)
	}
}

func sshOptions(settings Settings) []string {
	options := []string{"-o", fmt.Sprint("ConnectTimeout=", DEFAULT_CONNECT_TIMEOUT), "-o", "LogLevel=ERROR"}
	options = append(options, "-o", fmt.Sprint("ServerAliveInterval=", SERVER_ALIVE_INTERVAL))
	options = append(options, "-o", fmt.Sprint("ServerAliveCountMax=", SERVER_ALIVE_COUNT_MAX))

	// Batch mode settings for ssh to prevent it from asking its' stupid questions
	options = append(options, "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no")
	options = append(options, "-o", "UserKnownHostsFile=/dev/null")

	if settings.port > 0 {
		options = append(options, "-o", fmt.Sprintf("Port=%d", settings.port))
	}
	if settings.username != "" {
		options = append(options, "-o", "User="+settings.username)
	}
	if settings.compression {
		options = append(options, "-o", "Compression=yes")
	}

	return options
}

func execOrPanic(cmd string, args []string) string {
	debugLn(cmd, args)
	var bufErr bytes.Buffer
	command := exec.Command(cmd, args...)
	command.Stderr = &bufErr
	output, err := command.Output()

	if err != nil {
		progressLn("Cannot ", cmd, " ", args, ", got error: ", err.Error())
		progressLn("Command output:\n", string(output), "\nstderr:\n", bufErr.String())
		panic("Command exited with non-zero code")
	}

	return string(output)
}

func formatLength(len int) string {
	if len < 1024 {
		return fmt.Sprintf("%d B", len)
	} else if len < 1048576 {
		return fmt.Sprintf("%d KiB", len/1024)
	} else {
		return fmt.Sprintf("%d MiB", len/1048576)
	}
}
