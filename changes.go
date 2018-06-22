package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/unrealsync/fswatcher"
)

var (
	repo         map[string]map[string]*UnrealStat
	localDiff    [maxDiffSize]byte
	localDiffPtr int
)

func waitWatcherReady(fschanges chan string) {
	debugLn("Waiting for watcher")
	for {
		change := <-fschanges
		debugLn("got ", string(change), " from Watcher")
		if change == fswatcher.LOCAL_WATCHER_READY {
			progressLn("Watcher ready")
			break
		}
	}
}

func commitDiff() {
	if localDiffPtr == 0 {
		return
	}

	buf := localDiff[0:localDiffPtr]
	writeToOutLog(actionDiff, buf)

	localDiffPtr = 0

	return
}

// Send big file in chunks:
// actionBigInit  = filename
// actionBigRcv   = filename length (10 bytes) | filename | chunk contents
// actionBigAbort = filename
func commitBigFile(fileStr string, stat *UnrealStat) {
	progressLn("Sending big file: ", fileStr, " (", stat.size/1024/1024, " MiB)")

	fp, err := os.Open(fileStr)
	if err != nil {
		progressLn("Could not open ", fileStr, ": ", err)
		return
	}
	defer fp.Close()

	dir := filepath.Dir(fileStr)
	if _, ok := repo[dir]; !ok {
		repo[dir] = make(map[string]*UnrealStat)
	}
	repo[dir][filepath.Base(fileStr)] = stat

	file := []byte(fileStr)

	writeToOutLog(actionBigInit, file)
	bytesLeft := stat.size

	for {
		buf := make([]byte, maxDiffSize/2)
		bufOffset := 0

		copy(buf[bufOffset:10], fmt.Sprintf("%010d", len(file)))
		bufOffset += 10

		copy(buf[bufOffset:len(file)+bufOffset], file)
		bufOffset += len(file)

		fileStat, err := fp.Stat()
		if err != nil {
			progressLn("Cannot stat ", fileStr, " that we are reading right now: ", err.Error())
			writeToOutLog(actionBigAbort, []byte(file))
			return
		}

		newStat := UnrealStatFromStat(fileStr, fileStat)
		if !StatsEqual(newStat, *stat) {
			progressLn("File ", fileStr, " has changed, aborting transfer")
			writeToOutLog(actionBigAbort, []byte(file))
			return
		}

		n, err := fp.Read(buf[bufOffset:])
		if err != nil && err != io.EOF {
			// if we were unable to read file that we just opened then probably there are some problems with the OS
			progressLn("Cannot read ", file, ": ", err)
			writeToOutLog(actionBigAbort, []byte(file))
			return
		}

		if n != len(buf)-bufOffset && int64(n) != bytesLeft {
			progressLn("Read different number of bytes than expected from ", file)
			writeToOutLog(actionBigAbort, []byte(file))
			return
		}

		writeToOutLog(actionBigRcv, buf[0:bufOffset+n])

		if bytesLeft -= int64(n); bytesLeft == 0 {
			break
		}
	}

	writeToOutLog(actionBigCommit, []byte(fmt.Sprintf("%010d%s%s", len(file), fileStr, stat.Serialize())))

	progressLn("Big file ", fileStr, " successfully sent")

	return
}

func addToDiff(file string, stat *UnrealStat) {
	var diffLen int64
	var buf, diffHeader []byte

	if stat == nil {
		diffHeader = []byte("D " + file + diffSep)
	} else {
		diffHeader = []byte("A " + file + "\n" + stat.Serialize() + diffSep)
		if stat.isDir == false {
			diffLen = stat.size
		}
	}

	if diffLen > maxDiffSize/2 {
		commitBigFile(file, stat)
		return
	}

	if localDiffPtr+int(diffLen)+len(diffHeader) >= maxDiffSize-1 {
		progressLn("Diff too big:", localDiffPtr+int(diffLen)+len(diffHeader), " >= ", maxDiffSize-1, " autocommit")
		commitDiff()
	}

	if stat != nil && diffLen > 0 {
		if stat.isLink {
			bufStr, err := os.Readlink(file)
			if err != nil {
				progressLn("Could not read link " + file)
				return
			}

			buf = []byte(bufStr)

			if len(buf) != int(diffLen) {
				progressLn("Readlink different number of bytes than expected from ", file)
				return
			}
		} else {
			fp, err := os.Open(file)
			if err != nil {
				progressLn("Could not open ", file, ": ", err)
				return
			}
			defer fp.Close()

			buf = make([]byte, diffLen)
			n, err := fp.Read(buf)
			if err != nil && err != io.EOF {
				// if we were unable to read file that we just opened then probably there are some problems with the OS
				progressLn("Cannot read ", file, ": ", err)
				return
			}

			if n != int(diffLen) {
				progressLn("Read different number of bytes than expected from ", file)
				return
			}
		}
	}

	localDiffPtr += copy(localDiff[localDiffPtr:], diffHeader)

	if stat != nil && diffLen > 0 {
		localDiffPtr += copy(localDiff[localDiffPtr:], buf)
	}

	return
}

func aggregateDirs(dirschan chan string, excludes map[string]bool) {
	dirs := make(map[string]bool)
	tick := time.Tick(dirAggregateInterval)

	for {
		select {
		case dir := <-dirschan:
			if dir, err := getPathToSync(dir, excludes); err == nil {
				dirs[dir] = true
			}

		case <-tick:
			if len(dirs) == 0 {
				continue
			}

			for dir := range dirs {
				progressLn("Changed dir: ", dir)
				syncDir(dir, false, true)
			}
			commitDiff()
			dirs = make(map[string]bool)
		}
	}
}

func getPathToSync(path string, excludes map[string]bool) (string, error) {
	var err error
	if filepath.IsAbs(path) {
		path, err = filepath.Rel(sourceDir, path)
		if err != nil {
			progressLn("Cannot compute relative path: ", err)
			return "", err
		}
	}
	stat, err := os.Lstat(path)

	if err != nil {
		if !os.IsNotExist(err) {
			progressLn("Stat failed for ", path, ": ", err.Error())
			return "", err
		}

		path = filepath.Dir(path)
	} else if !stat.IsDir() {
		path = filepath.Dir(path)
	}
	if pathIsGlobalExcluded(path, excludes) {
		return "", errors.New("excluded folder change")
	}
	return path, nil
}

func pathIsGlobalExcluded(path string, excludes map[string]bool) bool {
	if strings.HasPrefix(path, ".unrealsync") {
		return true
	}
	for exclude := range excludes {
		if strings.HasPrefix(path, exclude) {
			return true
		}
	}
	return false
}

func syncDir(dir string, recursive, sendChanges bool) {
	if strings.HasPrefix(dir, "./") {
		dir = dir[2:]
	}
	if dir == ".unrealsync" {
		return
	}

	fp, err := os.Open(dir)
	if err != nil {
		progressLn("Cannot open ", dir, ": ", err.Error())
		return
	}

	defer fp.Close()

	stat, err := fp.Stat()
	if err != nil {
		progressLn("Cannot stat ", dir, ": ", err.Error())
		return
	}

	if !stat.IsDir() {
		progressLn("Suddenly ", dir, " stopped being a directory")
		return
	}

	repoInfo, ok := repo[dir]
	if !ok {
		debugLn("No dir ", dir, " in repo")
		repoInfo = make(map[string]*UnrealStat)
		repo[dir] = repoInfo
	}

	// Detect deletions: we need to do it first because otherwise change from dir to file will be impossible
	for name := range repoInfo {
		_, err := os.Lstat(dir + "/" + name)
		if os.IsNotExist(err) {
			delete(repoInfo, name)
			debugLn("Deleted: ", dir, "/", name)
			if sendChanges {
				addToDiff(dir+"/"+name, nil)
			}
		} else if err != nil {
			fatalLn("Could not lstat ", dir, "/", name, ": ", err)
		}
	}

	for {
		res, err := fp.Readdir(10)
		if err != nil {
			if err == io.EOF {
				break
			}

			progressLn("Could not read directory names from " + dir + ": " + err.Error())
			break
		}

		for _, info := range res {
			repoEl, ok := repoInfo[info.Name()]
			filePath := filepath.Join(dir, info.Name())
			unrealStat := UnrealStatFromStat(filepath.Join(dir, info.Name()), info)

			if !ok || !StatsEqual(unrealStat, *repoEl) {
				if info.IsDir() && (recursive || !ok || !repoEl.isDir) {
					syncDir(filePath, true, sendChanges)
				}

				repoInfo[info.Name()] = &unrealStat

				prefix := "Changed: "
				if !ok {
					prefix = "Added: "
				}
				debugLn(prefix, filePath)
				if sendChanges {
					addToDiff(filePath, &unrealStat)
				} else if hashCheck { // todo: move repository initialization in separate method
					unrealStat.Hash() // to calculate hash when we initialize repository so that we will have some hashes on sync
				}
			}
		}
	}

	repo[dir] = repoInfo

	return
}

func pingThread() {
	for {
		writeToOutLog(actionPing, []byte(""))
		time.Sleep(pingInterval)
	}
}

func doClient(servers map[string]Settings, globalExcludes map[string]bool) {
	if len(servers) == 0 {
		servers, globalExcludes = parseConfig()
	}

	repo = make(map[string]map[string]*UnrealStat)

	clients := make(map[string]*Client)
	for key, settings := range servers {
		clients[key] = MakeClient(settings)
		go clients[key].startServer()

	}
	go pingThread()

	dirschan := make(chan string, 10000)
	go fswatcher.RunWatcher(sourceDir, dirschan)
	waitWatcherReady(dirschan)

	syncDir(".", true, false)
	go printStatusThread(clients)

	// read watcher
	progressLn("Entering watcher loop")
	aggregateDirs(dirschan, globalExcludes)
}
