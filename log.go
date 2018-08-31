package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	logMaxSize = 50 * 1048576
)

type SortableStrings []string

type BufBlocker struct {
	buf  []byte
	sent chan bool
}

var (
	outLogWriteFp     *os.File
	outLogPos         int64
	outLogReadFps     map[string]*os.File
	outLogReadPos     map[string]int64
	outLogReadOldSize map[string]int64
	outLogMutex       sync.Mutex
)

func (r SortableStrings) Len() int {
	return len(r)
}

func (r SortableStrings) Less(i, j int) bool {
	return strings.Compare(r[i], r[j]) > 0
}

func (r SortableStrings) Swap(i, j int) {
	r[i], r[j] = r[j], r[i]
}

func initializeLogs() {

	createOutLog()
	outLogReadFps = make(map[string]*os.File)
	outLogReadPos = make(map[string]int64)
	outLogReadOldSize = make(map[string]int64)
}

func writeToOutLog(action string, buf []byte) {
	outLogMutex.Lock()
	defer outLogMutex.Unlock()

	_, err := fmt.Fprintf(outLogWriteFp, "%s%10d%s", action, len(buf), buf)
	if err != nil {
		fatalLn(err)
	}

	outLogPos, err = outLogWriteFp.Seek(0, io.SeekCurrent)
	debugLn("outlogpos:", outLogPos, " after action:", action)
	if outLogPos > logMaxSize {
		for _, oldSize := range outLogReadOldSize {
			if oldSize != 0 {
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
	logFilePath := getLogFilePath(repoLogFilename)
	if outLogWriteFp != nil {
		outLogWriteFp.Close()
		os.Remove(logFilePath)
	}
	var err error
	outLogWriteFp, err = os.OpenFile(logFilePath, os.O_APPEND|os.O_WRONLY|os.O_TRUNC|os.O_CREATE, 0666)
	if err != nil {
		fatalLn("Cannot open ", logFilePath, ": ", err.Error())
	}
	for hostname := range outLogReadOldSize {
		// invalidate readers
		outLogReadOldSize[hostname] = outLogPos
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
	fp, err = os.Open(getLogFilePath(repoLogFilename))
	if err != nil {
		return
	}
	outLogReadFps[hostname] = fp

	if continuation {
		_, err = fp.Seek(outLogPos, io.SeekStart)
		if err != nil {
			return
		}
		outLogReadPos[hostname] = outLogPos
	} else {
		outLogReadPos[hostname] = 0
	}
	outLogReadOldSize[hostname] = 0
	return
}

func doSendChanges(stream chan BufBlocker, client *Client) {
	var err error
	buf := make([]byte, maxDiffSize+20) // maxDiffSize limits only diff itself, so extra action+len required, each 10 bytes
	var pos int64
	var bufLen int
	bufBlocker := BufBlocker{buf: buf, sent: make(chan bool)}

	hostname := client.settings.host

doSendChangesLoop:
	for {
		select {
		case <-client.stopCh:
			progressLn("Got stop sendChanges")
			break doSendChangesLoop
		default:
		}
		outLogMutex.Lock()
		localOutLogPos := outLogPos
		localOldSize := outLogReadOldSize[hostname]
		localReadPos := outLogReadPos[hostname]
		fp := outLogReadFps[hostname]
		outLogMutex.Unlock()

		if localReadPos == localOutLogPos && localOldSize == 0 {
			time.Sleep(time.Millisecond * 20)
			continue
		}

		bufLen, err = readLogEntry(fp, buf)
		if err == io.EOF {
			err = openOutLogForRead(hostname, false)
			if err != nil {
				sendErrorNonBlocking(client.errorCh, err)
				break
			}
			continue
		}
		if err != nil {
			sendErrorNonBlocking(client.errorCh, err)
			break
		}

		pos, err = fp.Seek(0, io.SeekCurrent)
		if err != nil {
			sendErrorNonBlocking(client.errorCh, err)
			break
		}

		bufBlocker.buf = buf[0:bufLen]
		select {
		case stream <- bufBlocker:
		case <-client.stopCh:
			progressLn("Got stop sendChanges2")
			break doSendChangesLoop
		}
		select {
		case <-bufBlocker.sent:
		case <-client.stopCh:
			progressLn("Got stop sendChanges3")
			break doSendChangesLoop
		}
		outLogMutex.Lock()
		outLogReadPos[hostname] = pos
		outLogMutex.Unlock()
		debugLn("hostname:", hostname, " pos:", pos, " after reading", string(buf[0:10]))
	}
}

func printStatusThread(clients map[string]*Client) {
	var sendQueueSize int64
	prevStatusesOk := false
	mem := new(runtime.MemStats)
	for {
		statuses := make([]string, 0)

		outLogMutex.Lock()
		for hostname, oldSize := range outLogReadOldSize {
			if oldSize != 0 {
				sendQueueSize = oldSize - outLogReadPos[hostname] + outLogPos
				statuses = append(statuses, hostname+" "+formatLength(int(sendQueueSize))+"*")
			} else if outLogReadPos[hostname] != outLogPos {
				sendQueueSize = outLogPos - outLogReadPos[hostname]
				statuses = append(statuses, hostname+" "+formatLength(int(sendQueueSize)))
			} else {
				sendQueueSize = 0
			}
			if err := clients[hostname].notifySendQueueSize(sendQueueSize); err != nil {
				progressLn("removing " + hostname + " from outLogReadOldSize")
				delete(outLogReadOldSize, hostname)
			}
		}
		outLogMutex.Unlock()

		sort.Sort(SortableStrings(statuses))

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

func getLogFilePath(relativePath string) string {
	return filepath.Join(repoPath, relativePath)
}
