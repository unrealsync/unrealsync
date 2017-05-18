package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type Client struct {
	logFp    *os.File
	settings Settings
}

var (
	repo         map[string]map[string]*UnrealStat
	localDiff    [MAX_DIFF_SIZE]byte
	localDiffPtr int
)

func initialServerSync(hostname string, settings Settings) (err error) {
	progressLn("Initial file sync using rsync at " + hostname + "...")

	// TODO: escaping
	args := []string{"-e", "ssh " + strings.Join(sshOptions(settings), " ")}
	for dir, _ := range settings.excludes {
		args = append(args, "--exclude="+dir)
	}

	err = openOutLogForRead(hostname, true)
	if err != nil {
		return
	}
	if settings.sudouser != "" {
		args = append(args, "--rsync-path", "sudo -u "+settings.sudouser+" rsync")
	}

	// TODO: escaping of remote dir
	//"--delete-excluded",
	args = append(args, "-a", "--delete", sourceDir+"/", settings.host+":"+settings.dir+"/")
	execOrPanic("rsync", args)
	return
}

func copyUnrealsyncBinaries(uname []string, settings Settings) {
	progressLn("Copying unrealsync binaries to " + settings.host + " ostype " + uname[0] + " osarch " + uname[1])

	ostypeLower := strings.ToLower(uname[0])

	names := []string{"/unrealsync-" + ostypeLower + "-" + uname[1]}
	if ostypeLower == "darwin" && false {
		names = append(names, "/notify-"+ostypeLower)
	}

	for _, name := range names {
		args := sshOptions(settings)
		source := unrealsyncDir + name

		if _, err := os.Stat(source); err != nil {
			panic("Cannot stat " + source + ": " + err.Error() +
				". Please make sure you have built a corresponding unrealsync server version for your remote OS")
		}

		destination := settings.host + ":" + settings.dir + "/.unrealsync" + strings.Split(name, "-")[0]
		args = append(args, source, destination)
		execOrPanic("scp", args)
	}
}

func startServer(hostname string, settings Settings) {
	var cmd *exec.Cmd
	var stdin io.WriteCloser
	var stdout io.ReadCloser
	defer func() {
		if err := recover(); err != nil {
			trace := make([]byte, 10000)
			bytes := runtime.Stack(trace, false)
			progressLn("Failed to start for server ", hostname, ": ", err, bytes, string(trace))
			if cmd != nil {
				err := cmd.Process.Kill()
				if err != nil {
					progressLn("Could not kill ssh process for " + hostname + ":" + err.Error())
					// no action
				}
				err = cmd.Wait()
				if err != nil {
					progressLn("Could not wait ssh process for " + hostname + ":" + err.Error())
				}
			}

			go func() {
				time.Sleep(RETRY_INTERVAL)
				progressLn("Reconnecting to " + hostname)
				startServer(hostname, settings)
			}()
		}
	}()

	initialServerSync(hostname, settings)
	uname := createDirectoriesAt(hostname, settings)
	if settings.remoteBinPath == "" {
		copyUnrealsyncBinaries(uname, settings)
	}
	cmd, stdin, stdout = launchUnrealsyncAt(settings)

	stopChan := make(chan bool)
	stream := make(chan BufBlocker)
	errorCh := make(chan error)
	// receive from singlestdinwriter (stream) and send into ssh stdin
	go singleStdinWriter(stream, stdin, errorCh)
	// read log and send into ssh stdin via singlestdinwriter (stream)
	// stops if stopChan closes and closes stream
	go doSendChanges(stream, hostname, stopChan, errorCh)
	// read ssh stdout and send into ssh stdin via singlestdinwriter (stream)
	go pingReplyThread(stdout, settings, stream, errorCh)

	err := <-errorCh
	close(stopChan)
	panic(err)
}

func launchUnrealsyncAt(settings Settings) (*exec.Cmd, io.WriteCloser, io.ReadCloser) {
	progressLn("Launching unrealsync at " + settings.host + "...")

	args := sshOptions(settings)
	// TODO: escaping
	flags := "--server --hostname=" + settings.host
	if isDebug {
		flags += " --debug"
	}
	for dir, _ := range settings.excludes {
		flags += " --excludes " + dir
	}

	var unrealsyncBinaryPath string
	if settings.remoteBinPath != "" {
		unrealsyncBinaryPath = settings.remoteBinPath
	} else {
		unrealsyncBinaryPath = settings.dir + "/.unrealsync/unrealsync"
	}
	unrealsyncLaunchCmd := unrealsyncBinaryPath + " " + flags + " " + settings.dir
	if settings.sudouser != "" {
		unrealsyncLaunchCmd = "sudo -u " + settings.sudouser + " " + unrealsyncLaunchCmd
	}
	args = append(args, settings.host, unrealsyncLaunchCmd)

	debugLn("ssh", args)
	cmd := exec.Command("ssh", args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fatalLn("Cannot get stdout pipe: ", err.Error())
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		fatalLn("Cannot get stdin pipe: ", err.Error())
	}

	cmd.Stderr = os.Stderr

	if err = cmd.Start(); err != nil {
		panic("Cannot start command ssh " + strings.Join(args, " ") + ": " + err.Error())
	}
	return cmd, stdin, stdout
}

func createDirectoriesAt(hostname string, settings Settings) []string {
	progressLn("Creating directories at " + hostname + "...")

	args := sshOptions(settings)
	// TODO: escaping
	dir := settings.dir + "/.unrealsync"
	args = append(args, settings.host, "if [ ! -d "+dir+" ]; then mkdir -p "+dir+"; fi; rm -f "+dir+"/unrealsync && uname && uname -m")

	output := execOrPanic("ssh", args)
	uname := strings.Split(strings.TrimSpace(output), "\n")
	return uname
}

func singleStdinWriter(stream chan BufBlocker, stdin io.WriteCloser, errorCh chan error) {
	for {
		bufBlocker := <-stream
		_, err := stdin.Write(bufBlocker.buf)
		bufBlocker.sent <- true
		if err != nil {
			errorCh <- err
			break
		}
	}
}

func pingReplyThread(stdout io.ReadCloser, settings Settings, stream chan BufBlocker, errorCh chan error) {
	bufBlocker := BufBlocker{buf: make([]byte, 20), sent: make(chan bool)}
	bufBlocker.buf = []byte(ACTION_PONG + fmt.Sprintf("%10d", 0))
	buf := make([]byte, 10)
	for {
		read_bytes, err := io.ReadFull(stdout, buf)
		if err != nil {
			errorCh <- errors.New("Could not read from server:" + hostname + " err:" + err.Error())
			break
		}
		debugLn("Read ", read_bytes, " from ", settings.host, " ", buf)
		stream <- bufBlocker
		<-bufBlocker.sent
	}
}

func startWatcher() *bufio.Reader {
	notifyPath := unrealsyncDir + "/notify-darwin"

	cmd := exec.Command(notifyPath, sourceDir)
	debugLn(notifyPath, sourceDir)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fatalLn("Cannot get stdout pipe: ", err.Error())
	}

	if err = cmd.Start(); err != nil {
		panic("Cannot start notify: " + err.Error())
	}

	return bufio.NewReader(stdout)
}

func waitWatcherReady(r *bufio.Reader) {
	debugLn("Waiting for watcher")
	for {
		lineBytes, _, err := r.ReadLine()
		debugLn("got ", string(lineBytes), " from Watcher")
		if err != nil {
			fatalLn("Could not read line from notify utility: " + err.Error())
		}
		if string(lineBytes) == LOCAL_WATCHER_READY {
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
			progressLn("Cannot stat ", fileStr, " that we are reading right now: ", err.Error())
			writeToOutLog(ACTION_BIG_ABORT, []byte(file))
			return
		}

		if !StatsEqual(fileStat, *stat) {
			progressLn("File ", fileStr, " has changed, aborting transfer")
			writeToOutLog(ACTION_BIG_ABORT, []byte(file))
			return
		}

		n, err := fp.Read(buf[bufOffset:])
		if err != nil && err != io.EOF {
			// if we were unable to read file that we just opened then probably there are some problems with the OS
			progressLn("Cannot read ", file, ": ", err)
			writeToOutLog(ACTION_BIG_ABORT, []byte(file))
			return
		}

		if n != len(buf)-bufOffset && int64(n) != bytesLeft {
			progressLn("Read different number of bytes than expected from ", file)
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
	diffHeaderStr := ""
	var diffLen int64
	var buf []byte

	if stat == nil {
		diffHeaderStr += "D " + file + DIFF_SEP
		//progressLn("D " + file)
	} else {
		diffHeaderStr += "A " + file + "\n" + stat.Serialize() + DIFF_SEP
		//progressLn("A " + file)
		if stat.isDir == false {
			diffLen = stat.size
		}
	}

	diffHeader := []byte(diffHeaderStr)

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

func aggregateDirs(dirschan chan string) {
	dirs := make(map[string]bool)
	tick := time.Tick(DIR_AGGREGATE_INTERVAL)

	for {
		select {
		case dir := <-dirschan:
			dirs[dir] = true

		case <-tick:
			if len(dirs) == 0 {
				continue
			}

			for dir, _ := range dirs {
				//progressLn("Changed dir: ", dir)
				syncDir(dir, false, true)
			}
			commitDiff()
			dirs = make(map[string]bool)
		}
	}
}

func syncDir(dir string, recursive, sendChanges bool) {
	if dir == "./.unrealsync" {
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

			progressLn("Could not read directory names from " + dir + ": " + err.Error())
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

func readWatcher(r *bufio.Reader, dirschan chan string) {
	for {
		lineBytes, _, err := r.ReadLine()
		if err != nil {
			fatalLn("Could not read line from notify utility: " + err.Error())
		}

		path := string(lineBytes)
		if filepath.IsAbs(path) {
			path, err = filepath.Rel(sourceDir, path)
			if err != nil {
				progressLn("Cannot compute relative path: ", err)
				continue
			}
		}

		stat, err := os.Lstat(path)

		if err != nil {
			if !os.IsNotExist(err) {
				progressLn("Stat failed for ", path, ": ", err.Error())
				continue
			}

			path = filepath.Dir(path)
		} else if !stat.IsDir() {
			path = filepath.Dir(path)
		}
		if strings.HasPrefix(path, ".unrealsync") {
			continue
		}

		dirschan <- path
	}
}

func doClient() {
	servers := parseConfig()

	repo = make(map[string]map[string]*UnrealStat)

	for key, settings := range servers {
		go startServer(key, settings)
	}
	go pingThread()

	r := startWatcher()

	waitWatcherReady(r)
	syncDir(".", true, false)
	go printStatusThread()

	// read watcher
	progressLn("Entering watcher loop")
	dirschan := make(chan string, 10000)
	go readWatcher(r, dirschan)
	aggregateDirs(dirschan)
}
