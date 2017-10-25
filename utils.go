package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

var Log [1000]string
var LogHead int

func progress(a []interface{}, withPref bool, andErr bool) {
	msg := ""
	if withPref {
		repeatLen := 15 - len(hostname)
		if repeatLen <= 0 {
			repeatLen = 1
		}
		now := time.Now()

		msg += fmt.Sprintf("%s.%09d ", now.Format("15:04:05"), now.Nanosecond()) +
			fmt.Sprint(" ", hostname, "$ ", strings.Repeat(" ", repeatLen))
	}
	msg += fmt.Sprint(a...)
	if withPref {
		msg += "\n"
	}

	if isServer || andErr {
		fmt.Fprint(os.Stderr, msg)
	}
	if !isServer {
		if LogHead >= len(Log) {
			LogHead = 0
		}
		Log[LogHead] = msg
		LogHead++
	}
}

func progressWithout(a ...interface{}) {
	progress(a, false, false)
}
func progressLn(a ...interface{}) {
	progress(a, true, false)
}

func warningLn(a ...interface{}) {
	progress(a, true, true)
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
	if settings.batchMode {
		options = append(options, "-o", "BatchMode=yes")
	}
	options = append(options, "-o", "StrictHostKeyChecking=no")
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

func execOrPanic(cmd string, args []string, cancelCh chan bool) string {
	debugLn(cmd, args)
	var bufErr bytes.Buffer
	command := exec.Command(cmd, args...)
	command.Stderr = &bufErr

	go func() {
		_, open := <-cancelCh
		if !open {
			err := command.Process.Kill()
			if err != nil {
				warningLn("Could not kill process on cancel:", cmd, args)
			}
		}
	}()
	output, err := command.Output()

	if err != nil {
		warningLn("Cannot ", cmd, " ", args, ", got error: ", err.Error())
		warningLn("Command output:\n", string(output), "\nstderr:\n", bufErr.String())
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
