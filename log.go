package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	LOG_MAX_SIZE = 50 * 1048576
)

type BufBlocker struct {
	buf  []byte
	sent chan bool
}

var (
	outLogWriteFp    *os.File
	outLogPos        int64
	outLogReadFps    map[string]*os.File
	outLogReadPos    map[string]int64
	outLogReadActual map[string]bool
	outLogMutex      sync.Mutex
)

func initializeLogs() {
	createOutLog()
	outLogReadFps = make(map[string]*os.File)
	outLogReadPos = make(map[string]int64)
	outLogReadActual = make(map[string]bool)
}

func writeToOutLog(action string, buf []byte) {
	outLogMutex.Lock()
	defer outLogMutex.Unlock()

	_, err := fmt.Fprintf(outLogWriteFp, "%s%10d%s", action, len(buf), buf)
	if err != nil {
		fatalLn(err)
	}

	outLogPos, err = outLogWriteFp.Seek(0, os.SEEK_CUR)
	debugLn("outlogpos:", outLogPos, " after action:", action)
	if outLogPos > LOG_MAX_SIZE {
		for _, actual := range outLogReadActual {
			if !actual {
				debugLn("could not reopen log, not all readers are reading from actual")
				return
			}
		}
		progressLn("Rotating outlog")
		createOutLog()
	}
	return
}

func createOutLog() {
	if outLogWriteFp != nil {
		outLogWriteFp.Close()
		os.Remove(REPO_LOG_FILENAME)
	}
	var err error
	outLogWriteFp, err = os.OpenFile(REPO_LOG_FILENAME, os.O_APPEND|os.O_WRONLY|os.O_TRUNC|os.O_CREATE, 0666)
	if err != nil {
		fatalLn("Cannot open ", REPO_LOG_FILENAME, ": ", err.Error())
	}
	outLogWriteFp.Name()
	for hostname, _ := range outLogReadActual {
		// invalidate readers
		outLogReadActual[hostname] = false
	}
	outLogPos = 0
}

func openOutLogForRead(hostname string, continuation bool) (err error) {
	outLogMutex.Lock()
	defer outLogMutex.Unlock()
	fp, ok := outLogReadFps[hostname]
	if ok {
		progressLn("Closing old log fp for ", hostname)
		err = fp.Close()
		if err != nil {
			return
		}
	}
	progressLn("Opening log for ", hostname)
	fp, err = os.Open(REPO_LOG_FILENAME)
	if err != nil {
		return
	}
	outLogReadFps[hostname] = fp

	if continuation {
		_, err = fp.Seek(outLogPos, os.SEEK_SET)
		if err != nil {
			return
		}
		outLogReadPos[hostname] = outLogPos
	} else {
		outLogReadPos[hostname] = 0
	}
	outLogReadActual[hostname] = true
	return
}

func doSendChanges(stream chan BufBlocker, hostname string, stopChan chan bool, errorCh chan error) {
	var err error
	buf := make([]byte, MAX_DIFF_SIZE+20) // MAX_DIFF_SIZE limits only diff itself, so extra action+len required, each 10 bytes
	var pos int64
	var bufLen int
	bufBlocker := BufBlocker{buf: buf, sent: make(chan bool)}

doSendChangesLoop:
	for {
		select {
		case <-stopChan:
			progressLn("Got stop sendChanges")
			break doSendChangesLoop
		default:
		}
		outLogMutex.Lock()
		localOutLogPos := outLogPos
		localActual := outLogReadActual[hostname]
		localReadPos := outLogReadPos[hostname]
		fp := outLogReadFps[hostname]
		outLogMutex.Unlock()

		if localReadPos == localOutLogPos && localActual {
			time.Sleep(time.Millisecond * 20)
			continue
		}

		bufLen, err = readLogEntry(fp, buf)
		if err == io.EOF {
			err = openOutLogForRead(hostname, false)
			if err != nil {
				errorCh <- err
				break
			}
			continue
		}
		if err != nil {
			errorCh <- err
			break
		}

		pos, err = fp.Seek(0, os.SEEK_CUR)
		if err != nil {
			errorCh <- err
			break
		}
		outLogMutex.Lock()
		outLogReadPos[hostname] = pos
		outLogMutex.Unlock()
		debugLn("hostname:", hostname, " pos:", pos, " after reading", string(buf[0:10]))

		bufBlocker.buf = buf[0:bufLen]
		select {
		case stream <- bufBlocker:
		case <-stopChan:
			progressLn("Got stop sendChanges2")
			break doSendChangesLoop
		}
		select {
		case <-bufBlocker.sent:
		case <-stopChan:
			progressLn("Got stop sendChanges3")
			break doSendChangesLoop
		}
	}
	close(stream)
}

func printStatusThread() {
	prevStatusesOk := false
	mem := new(runtime.MemStats)
	for {
		statuses := make([]string, 0)

		outLogMutex.Lock()
		for hostname, actual := range outLogReadActual {
			if !actual {
				statuses = append(statuses, hostname+" oldfile")
			} else if outLogReadPos[hostname] != outLogPos {
				statuses = append(statuses, hostname+" "+formatLength(int(outLogPos-outLogReadPos[hostname])))
			}
		}
		outLogMutex.Unlock()

		runtime.ReadMemStats(mem)
		if len(statuses) > 0 {
			progress("Pending diffs: ", strings.Join(statuses, "; "))
			prevStatusesOk = false
		} else if !prevStatusesOk {
			progress("All diffs were sent mem.Sys:", formatLength(int(mem.Sys)))
			prevStatusesOk = true
		}
		time.Sleep(time.Millisecond * 300)
	}
}

// read a single entry from log into buf
func readLogEntry(fp *os.File, buf []byte) (bufLen int, err error) {
	outLogMutex.Lock()
	defer outLogMutex.Unlock()

	var n, diffLen int
	// action
	n, err = io.ReadFull(fp, buf[0:10])
	if err != nil {
		return
	}
	if n != 10 {
		err = errors.New("read into action unexpected bytes:" + fmt.Sprint(n) + " instead of 10")
		return
	}
	// diff length
	n, err = io.ReadFull(fp, buf[10:20])
	if err != nil {
		return
	}
	if n != 10 {
		err = errors.New("read into len unexpected bytes:" + fmt.Sprint(n) + " instead of 10")
		return
	}
	// diff
	diffLen, err = strconv.Atoi(strings.TrimSpace(string(buf[10:20])))
	if err != nil {
		return
	}

	// no payload as for PING
	if diffLen == 0 {
		bufLen = 20
		return
	}

	n, err = io.ReadFull(fp, buf[20:20+diffLen])
	if err != nil {
		return
	}
	if n != diffLen {
		err = errors.New("read into buf unexpected bytes:" + fmt.Sprint(n) + " instead of " + fmt.Sprint(diffLen))
		return
	}
	bufLen = 20 + diffLen
	return
}
