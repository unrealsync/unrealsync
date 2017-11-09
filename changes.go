package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/unrealsync/fswatcher"
)

var (
	repo         map[string]map[string]*UnrealStat
	localDiff    [MAX_DIFF_SIZE]byte
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
	writeToOutLog(ACTION_DIFF, buf)

	localDiffPtr = 0

	return
}

// Send big file in chunks:
// ACTION_BIG_INIT  = filename
// ACTION_BIG_RCV   = filename length (10 bytes) | filename | chunk contents
// ACTION_BIG_ABORT = filename
func commitBigFile(fileStr string, stat *UnrealStat) {
	progressLn("Sending big file: ", fileStr, " (", (stat.size / 1024 / 1024), " MiB)")

	fp, err := os.Open(fileStr)
	if err != nil {
		warningLn("Could not open ", fileStr, ": ", err)
		return
	}
	defer fp.Close()

	dir := filepath.Dir(fileStr)
	if _, ok := repo[dir]; !ok {
		repo[dir] = make(map[string]*UnrealStat)
	}
	repo[dir][filepath.Base(fileStr)] = stat

	file := []byte(fileStr)

	writeToOutLog(ACTION_BIG_INIT, file)
	bytesLeft := stat.size

	for {
		buf := make([]byte, MAX_DIFF_SIZE/2)
		bufOffset := 0

		copy(buf[bufOffset:10], fmt.Sprintf("%010d", len(file)))
		bufOffset += 10

		copy(buf[bufOffset:len(file)+bufOffset], file)
		bufOffset += len(file)

		fileStat, err := fp.Stat()
		if err != nil {
			warningLn("Cannot stat ", fileStr, " that we are reading right now: ", err.Error())
			writeToOutLog(ACTION_BIG_ABORT, []byte(file))
			return
		}

		if !StatsEqual(fileStat, *stat) {
			warningLn("File ", fileStr, " has changed, aborting transfer")
			writeToOutLog(ACTION_BIG_ABORT, []byte(file))
			return
		}

		n, err := fp.Read(buf[bufOffset:])
		if err != nil && err != io.EOF {
			// if we were unable to read file that we just opened then probably there are some problems with the OS
			warningLn("Cannot read ", file, ": ", err)
			writeToOutLog(ACTION_BIG_ABORT, []byte(file))
			return
		}

		if n != len(buf)-bufOffset && int64(n) != bytesLeft {
			warningLn("Read different number of bytes than expected from ", file)
			writeToOutLog(ACTION_BIG_ABORT, []byte(file))
			return
		}

		writeToOutLog(ACTION_BIG_RCV, buf[0:bufOffset+n])

		if bytesLeft -= int64(n); bytesLeft == 0 {
			break
		}
	}

	writeToOutLog(ACTION_BIG_COMMIT, []byte(fmt.Sprintf("%010d%s%s", len(file), fileStr, stat.Serialize())))

	progressLn("Big file ", fileStr, " successfully sent")

	return
}

func addToDiff(file string, stat *UnrealStat) {
	var diffLen int64
	var buf, diffHeader []byte

	if stat == nil {
		diffHeader = []byte("D " + file + DIFF_SEP)
	} else {
		diffHeader = []byte("A " + file + "\n" + stat.Serialize() + DIFF_SEP)
		if stat.isDir == false {
			diffLen = stat.size
		}
	}

	if diffLen > MAX_DIFF_SIZE/2 {
		commitBigFile(file, stat)
		return
	}

	if localDiffPtr+int(diffLen)+len(diffHeader) >= MAX_DIFF_SIZE-1 {
		progressLn("Diff too big:", localDiffPtr+int(diffLen)+len(diffHeader), " >= ", MAX_DIFF_SIZE-1, " autocommit")
		commitDiff()
	}

	if stat != nil && diffLen > 0 {
		if stat.isLink {
			bufStr, err := os.Readlink(file)
			if err != nil {
				warningLn("Could not read link " + file)
				return
			}

			buf = []byte(bufStr)

			if len(buf) != int(diffLen) {
				warningLn("Readlink different number of bytes than expected from ", file)
				return
			}
		} else {
			fp, err := os.Open(file)
			if err != nil {
				warningLn("Could not open ", file, ": ", err)
				return
			}
			defer fp.Close()

			buf = make([]byte, diffLen)
			n, err := fp.Read(buf)
			if err != nil && err != io.EOF {
				// if we were unable to read file that we just opened then probably there are some problems with the OS
				warningLn("Cannot read ", file, ": ", err)
				return
			}

			if n != int(diffLen) {
				warningLn("Read different number of bytes than expected from ", file)
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
	tick := time.Tick(DIR_AGGREGATE_INTERVAL)

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

			for dir, _ := range dirs {
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
			warningLn("Cannot compute relative path: ", err)
			return "", err
		}
	}
	stat, err := os.Lstat(path)

	if err != nil {
		if !os.IsNotExist(err) {
			warningLn("Stat failed for ", path, ": ", err.Error())
			return "", err
		}

		path = filepath.Dir(path)
	} else if !stat.IsDir() {
		path = filepath.Dir(path)
	}
	if pathIsGlobalExcluded(path, excludes) {
		return "", errors.New("Excluded folder change")
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
		warningLn("Cannot open ", dir, ": ", err.Error())
		return
	}

	defer fp.Close()

	stat, err := fp.Stat()
	if err != nil {
		warningLn("Cannot stat ", dir, ": ", err.Error())
		return
	}

	if !stat.IsDir() {
		warningLn("Suddenly ", dir, " stopped being a directory")
		return
	}

	repoInfo, ok := repo[dir]
	if !ok {
		debugLn("No dir ", dir, " in repo")
		repoInfo = make(map[string]*UnrealStat)
		repo[dir] = repoInfo
	}

	// Detect deletions: we need to do it first because otherwise change from dir to file will be impossible
	for name, _ := range repoInfo {
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

			warningLn("Could not read directory names from " + dir + ": " + err.Error())
			break
		}

		for _, info := range res {
			repoEl, ok := repoInfo[info.Name()]
			if !ok || !StatsEqual(info, *repoEl) {
				if info.IsDir() && (recursive || !ok || !repoEl.isDir) {
					syncDir(dir+"/"+info.Name(), true, sendChanges)
				}

				unrealStat := UnrealStatFromStat(info)

				repoInfo[info.Name()] = &unrealStat

				prefix := "Changed: "
				if !ok {
					prefix = "Added: "
				}
				debugLn(prefix, dir, "/", info.Name())
				if sendChanges {
					addToDiff(dir+"/"+info.Name(), &unrealStat)
				}
			}
		}
	}

	repo[dir] = repoInfo

	return
}

func pingThread() {
	for {
		writeToOutLog(ACTION_PING, []byte(""))
		time.Sleep(PING_INTERVAL)
	}
}

func doClient(servers map[string]Settings) {
	var globalExcludes map[string]bool
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

	http.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		var sendQueueSize int64
		mem := new(runtime.MemStats)
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
			rw.Write([]byte("Pending diffs: " + strings.Join(statuses, "; ") + "\n"))
		} else {
			rw.Write([]byte("All diffs were sent mem.Sys:" + formatLength(int(mem.Sys)) + "\n"))
		}
		i := LogHead - 1
		for cnt := 1000; cnt > 0; cnt-- {
			if i < 0 {
				i = len(Log) - 1
			}
			rw.Write([]byte(Log[i]))
			i--
		}
	})
	go http.ListenAndServe("0.0.0.0:6061", nil)

	// read watcher
	warningLn("Entering watcher loop http://127.0.0.1:6061")
	aggregateDirs(dirschan, globalExcludes)
}
