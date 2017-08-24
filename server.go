package main

import (
	"bytes"
	"crypto/md5"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type (
	BigFile struct {
		fp      *os.File
		tmpName string
	}
)

var excludes map[string]bool

func applyDiff(buf []byte) {
	var (
		sepBytes = []byte(DIFF_SEP)
		offset   = 0
		endPos   = 0
	)

	dirs := make(map[string]map[string]*UnrealStat)

	for {
		if offset >= len(buf)-1 {
			break
		}

		if endPos = bytes.Index(buf[offset:], sepBytes); endPos < 0 {
			break
		}

		endPos += offset
		chunk := buf[offset:endPos]
		offset = endPos + len(sepBytes)
		op := chunk[0]

		var (
			diffstat UnrealStat
			file     []byte
			contents []byte
		)

		if op == 'A' {
			firstLinePos := bytes.IndexByte(chunk, '\n')
			if firstLinePos < 0 {
				fatalLn("No new line in file diff: ", string(chunk))
			}

			file = chunk[2:firstLinePos]
			diffstat = UnrealStatUnserialize(string(chunk[firstLinePos+1:]))
		} else if op == 'D' {
			file = chunk[2:]
		} else {
			fatalLn("Unknown operation in diff: ", op)
		}

		// TODO: path check

		if op == 'A' && !diffstat.isDir && diffstat.size > 0 {
			contents = buf[offset : offset+int(diffstat.size)]
			offset += int(diffstat.size)
		}

		fileStr := string(file)
		dir := path.Dir(fileStr)

		if dirs[dir] == nil {
			dirs[dir] = make(map[string]*UnrealStat)
		}

		if op == 'A' {
			writeContents(fileStr, diffstat, contents)
			dirs[dir][path.Base(fileStr)] = &diffstat
		} else if op == 'D' {
			err := os.RemoveAll(string(file))
			if err != nil {
				// TODO: better error handling than just print :)
				progressLn("Cannot remove ", string(file))
			}
			dirs[dir][path.Base(fileStr)] = nil
		} else {
			fatalLn("Unknown operation in diff:", op)
		}
	}
}

func readResponse(inStream io.ReadCloser) []byte {
	lengthBytes := make([]byte, 10)

	if _, err := io.ReadFull(inStream, lengthBytes); err != nil {
		panic("Cannot read diff length in applyThread from " + hostname + ": " + err.Error())
	}

	length, err := strconv.Atoi(strings.TrimSpace(string(lengthBytes)))
	if err != nil {
		panic("Incorrect diff length in applyThread from " + hostname + ": " + err.Error())
	}

	buf := make([]byte, length)
	if length == 0 {
		return buf
	}

	if length > MAX_DIFF_SIZE {
		panic("Too big diff from " + hostname + ", probably communication error")
	}

	if _, err := io.ReadFull(inStream, buf); err != nil {
		panic("Cannot read diff from " + hostname)
	}

	return buf
}

func applyThread(inStream io.ReadCloser) {
	bigFps := make(map[string]BigFile)

	defer func() {
		for _, bigFile := range bigFps {
			bigFile.fp.Close()
			os.Remove(bigFile.tmpName)
		}

		if r := recover(); r != nil {
			fatalLn("Error occured for ", hostname, ": ", r)
		}
	}()

	action := make([]byte, 10)

	mem := new(runtime.MemStats)

	for {
		_, err := io.ReadFull(inStream, action)
		if err != nil {
			panic("Cannot read action in applyThread from " + hostname + ": " + err.Error())
		}

		actionStr := string(action)
		runtime.ReadMemStats(mem)

		debugLn("Received ", "'"+actionStr+"' mem.Sys:", formatLength(int(mem.Sys)))
		rcvchan <- true

		buf := readResponse(inStream)

		if actionStr == ACTION_PING {
			os.Stdout.Write([]byte(ACTION_PONG))
		} else if actionStr == ACTION_DIFF {
			applyRemoteDiff(buf)
		} else if actionStr == ACTION_BIG_INIT {
			processBigInit(buf, bigFps)
		} else if actionStr == ACTION_BIG_RCV {
			processBigRcv(buf, bigFps)
		} else if actionStr == ACTION_BIG_COMMIT {
			processBigCommit(buf, bigFps)
		} else if actionStr == ACTION_BIG_ABORT {
			processBigAbort(buf, bigFps)
		} else if actionStr == ACTION_PONG {
		} else {
			debugLn("Unknown action", actionStr)
		}
	}
}

func tmpBigName(filename string) string {
	h := md5.New()
	io.WriteString(h, filename)
	return REPO_TMP + "big_" + fmt.Sprintf("%x", h.Sum(nil))
}

func processBigInit(buf []byte, bigFps map[string]BigFile) {
	filename := string(buf)
	tmpName := tmpBigName(filename)
	fp, err := os.OpenFile(tmpName, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0777)
	if err != nil {
		panic("Cannot open tmp file " + tmpName + ": " + err.Error())
	}

	bigFps[filename] = BigFile{fp, tmpName}
}
func processBigRcv(buf []byte, bigFps map[string]BigFile) {
	bufOffset := 0

	filenameLen, err := strconv.ParseInt(string(buf[bufOffset:10]), 10, 32)
	if err != nil {
		panic("Cannot parse big filename length")
	}

	bufOffset += 10
	filename := string(buf[bufOffset : bufOffset+int(filenameLen)])
	bufOffset += int(filenameLen)

	bigFile, ok := bigFps[filename]
	if !ok {
		panic("Received big chunk for unknown file: " + filename)
	}

	if _, err = bigFile.fp.Write(buf[bufOffset:]); err != nil {
		panic("Cannot write to tmp file " + bigFile.tmpName + ": " + err.Error())
	}
}

func processBigCommit(buf []byte, bigFps map[string]BigFile) {
	bufOffset := 0

	filenameLen, err := strconv.ParseInt(string(buf[bufOffset:10]), 10, 32)
	if err != nil {
		panic("Cannot parse big filename length")
	}

	bufOffset += 10
	filename := string(buf[bufOffset : bufOffset+int(filenameLen)])
	bufOffset += int(filenameLen)

	bigFile, ok := bigFps[filename]
	if !ok {
		panic("Received big commit for unknown file: " + filename)
	}

	bigstat := UnrealStatUnserialize(string(buf[bufOffset:]))
	if err = bigFile.fp.Close(); err != nil {
		panic("Cannot close tmp file " + bigFile.tmpName + ": " + err.Error())
	}

	if err = os.Chmod(bigFile.tmpName, os.FileMode(bigstat.mode)); err != nil {
		panic("Cannot chmod " + bigFile.tmpName + ": " + err.Error())
	}

	if err = os.Chtimes(bigFile.tmpName, time.Unix(bigstat.mtime, 0), time.Unix(bigstat.mtime, 0)); err != nil {
		panic("Cannot set mtime for " + bigFile.tmpName + ": " + err.Error())
	}

	os.MkdirAll(filepath.Dir(filename), 0777)
	if err = os.Rename(bigFile.tmpName, filename); err != nil {
		panic("Cannot rename " + bigFile.tmpName + " to " + filename + ": " + err.Error())
	}
}

func processBigAbort(buf []byte, bigFps map[string]BigFile) {
	filename := string(buf)
	bigFile, ok := bigFps[filename]
	if !ok {
		panic("Received big commit for unknown file: " + filename)
	}

	bigFile.fp.Close()
	os.Remove(bigFile.tmpName)
}

func applyRemoteDiff(buf []byte) {
	applyDiff(buf)
	progressLn("Applied diff ", formatLength(len(buf)))
}

func writeContents(file string, unrealStat UnrealStat, contents []byte) {
	stat, err := os.Lstat(file)

	if err == nil {
		// file already exists, we must delete it if it is symlink or dir because of inability to make atomic rename
		if stat.IsDir() != unrealStat.isDir || stat.Mode()&os.ModeSymlink == os.ModeSymlink {
			if err = os.RemoveAll(file); err != nil {
				progressLn("Cannot remove ", file, ": ", err.Error())
				return
			}
		}
	} else if !os.IsNotExist(err) {
		progressLn("Error doing lstat for ", file, ": ", err.Error())
		return
	}

	if unrealStat.isDir {
		if err = os.MkdirAll(file, 0777); err != nil {
			progressLn("Cannot create dir ", file, ": ", err.Error())
			return
		}
		if err = os.Chmod(file, os.FileMode(unrealStat.mode)); err != nil {
			progressLn("Cannot chmod dir ", file, ": ", err.Error())
			return
		}
	} else if unrealStat.isLink {
		if err = os.Symlink(string(contents), file); err != nil {
			progressLn("Cannot create symlink ", file, ": ", err.Error())
			return
		}
	} else {
		writeFile(file, unrealStat, contents)
	}
}

func writeFile(file string, unrealStat UnrealStat, contents []byte) {
	tempnam := REPO_TMP + path.Base(file)

	fp, err := os.OpenFile(tempnam, os.O_CREATE|os.O_TRUNC|os.O_RDWR, os.FileMode(unrealStat.mode))
	if err != nil {
		progressLn("Cannot open ", tempnam)
		return
	}

	if _, err = fp.Write(contents); err != nil {
		// TODO: more accurate error handling
		progressLn("Cannot write contents to ", tempnam, ": ", err.Error())
		fp.Close()
		return
	}

	if err = fp.Chmod(os.FileMode(unrealStat.mode)); err != nil {
		progressLn("Cannot chmod ", tempnam, ": ", err.Error())
		fp.Close()
		return
	}

	fp.Close()

	dir := path.Dir(file)
	if err = os.MkdirAll(dir, 0777); err != nil {
		progressLn("Cannot create dir ", dir, ": ", err.Error())
		os.Remove(tempnam)
		return
	}

	if err = os.Chtimes(tempnam, time.Unix(unrealStat.mtime, 0), time.Unix(unrealStat.mtime, 0)); err != nil {
		progressLn("Failed to change modification time for ", file, ": ", err.Error())
	}

	if err = os.Rename(tempnam, file); err != nil {
		progressLn("Cannot rename ", tempnam, " to ", file)
		os.Remove(tempnam)
		return
	}

	if isDebug {
		debugLn("Wrote ", file, " ", unrealStat.Serialize())
	}
}

func timeoutThread() {
	for {
		select {
		case <-rcvchan:
		case <-time.After(PING_INTERVAL * 2):
			progressLn("Server timeout")
			os.Exit(1)
		}
	}
}

func doServer() {
	excludes = make(map[string]bool)
	for _, dir := range excludesFlag {
		excludes[dir] = true
	}
	go applyThread(os.Stdin)
	go timeoutThread()
	progressLn("Entering ping loop")
	for {
		os.Stdout.Write([]byte(ACTION_PING))
		time.Sleep(PING_INTERVAL)
	}
}
