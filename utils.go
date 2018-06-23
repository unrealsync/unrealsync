package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func _progress(prefix string, a []interface{}, withEol bool) {
	repeatLen := 15 - len(prefix)
	if repeatLen <= 0 {
		repeatLen = 1
	}
	now := time.Now()

	msg := "\r\033[2K"
	msg += fmt.Sprintf("%s.%09d ", now.Format("15:04:05"), now.Nanosecond())
	msg += fmt.Sprint(" ", prefix, "$ ", strings.Repeat(" ", repeatLen))
	msg += fmt.Sprint(a...)
	if withEol {
		msg += fmt.Sprint("\n")
	}
	fmt.Fprint(os.Stderr, msg)
}

func progress(a ...interface{}) {
	_progress(hostname, a, false)
}

func progressLn(a ...interface{}) {
	_progress(hostname, a, true)
}

func progressWithPrefix(prefix string, a ...interface{}) {
	_progress(prefix, a, false)
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
	options := []string{"-o", fmt.Sprint("ConnectTimeout=", defaultConnectTimeout), "-o", "LogLevel=ERROR"}
	options = append(options, "-o", fmt.Sprint("ServerAliveInterval=", serverAliveInterval))
	options = append(options, "-o", fmt.Sprint("ServerAliveCountMax=", serverAliveCountMax))

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

	go killOnStop(command, cancelCh)
	output, err := command.Output()

	if err != nil {
		progressLn("Cannot ", cmd, " ", args, ", got error: ", err.Error())
		progressLn("Command output:\n", string(output), "\nstderr:\n", bufErr.String())
		panic("Command exited with non-zero code")
	}

	return string(output)
}

func killOnStop(command *exec.Cmd, stopChannel chan bool) {
	for {
		select {
		case <-stopChannel:
			err := command.Process.Kill()
			if err != nil {
				progressLn("Could not kill process on cancel: ", err.Error())
			}
			return
		default:
		}
		// state might be nil in the time before we call .Wait() or .Run()
		if state := command.ProcessState; state != nil && state.Exited() {
			return
		}
	}
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

func sendErrorNonBlocking(errorCh chan error, err error) {
	select {
	case errorCh <- err:
	default:
	}
}

func isCompatibleVersions(first, second string) bool {
	firstIntVersion, err := versionToIntArray(first)
	if err != nil {
		return false
	}
	secondIntVersion, err := versionToIntArray(second)
	if err != nil {
		return false
	}
	if len(firstIntVersion) < 2 || len(secondIntVersion) < 2 {
		return false
	}
	return firstIntVersion[0] == secondIntVersion[0] && firstIntVersion[1] == secondIntVersion[1]
}

func versionToIntArray(version string) ([]int, error) {
	stringVersion := strings.Split(version, ".")
	intVersion := make([]int, len(stringVersion))
	for k, stringPart := range stringVersion {
		intPart, err := strconv.Atoi(stringPart)
		if err != nil {
			return []int{}, err
		}
		intVersion[k] = intPart
	}
	return intVersion, nil
}
