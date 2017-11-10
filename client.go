package main

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

type Client struct {
	settings Settings
	stopCh   chan bool
	errorCh  chan error
}

func MakeClient(settings Settings) *Client {
	return &Client{settings: settings}
}

func (r *Client) initialServerSync() (err error) {
	progressLn("Initial file sync using rsync at " + r.settings.host + "...")

	// TODO: escaping
	args := []string{"-e", "ssh " + strings.Join(sshOptions(r.settings), " ")}
	for dir, _ := range r.settings.excludes {
		args = append(args, "--exclude="+dir)
	}

	err = openOutLogForRead(r.settings.host, true)
	if err != nil {
		return
	}
	if r.settings.sudouser != "" {
		args = append(args, "--rsync-path", "sudo -u "+r.settings.sudouser+" rsync")
	}

	// TODO: escaping of remote dir
	//"--delete-excluded",
	args = append(args, "-a", "--delete", sourceDir+"/", r.settings.host+":"+r.settings.dir+"/")
	execOrPanic("rsync", args, r.stopCh)
	return
}

func (r *Client) copyUnrealsyncBinaries(unrealsyncBinaryPathForHost string) {
	progressLn("Copying unrealsync binary " + unrealsyncBinaryPathForHost + " to " + r.settings.host)
	args := sshOptions(r.settings)
	destination := r.settings.host + ":" + r.settings.dir + "/.unrealsync/unrealsync"
	args = append(args, unrealsyncBinaryPathForHost, destination)
	execOrPanic("scp", args, r.stopCh)
}

func (r *Client) startServer() {
	r.stopCh = make(chan bool)
	r.errorCh = make(chan error)
	var cmd *exec.Cmd
	var stdin io.WriteCloser
	var stdout, stderr io.ReadCloser
	defer func() {
		if err := recover(); err != nil {
			trace := make([]byte, 10000)
			bytes := runtime.Stack(trace, false)
			warningLn("Failed to start for server ", r.settings.host, ": ", err, bytes, string(trace))
			if cmd != nil {
				err := cmd.Process.Kill()
				if err != nil {
					warningLn("Could not kill ssh process for " + r.settings.host + ":" + err.Error())
					// no action
				}
				err = cmd.Wait()
				if err != nil {
					warningLn("Could not wait ssh process for " + r.settings.host + ":" + err.Error())
				}
			}

			go func() {
				time.Sleep(RETRY_INTERVAL)
				warningLn("Reconnecting to " + r.settings.host)
				r.startServer()
			}()
		}
	}()

	r.initialServerSync()
	ostype, osarch, unrealsyncBinaryPath, unrealsyncVersion := r.createDirectoriesAt()
	progressLn("Discovered ostype:" + ostype + " osarch:" + osarch + " binary:" + unrealsyncBinaryPath + " version:" + unrealsyncVersion + " at " + r.settings.host)
	if r.settings.remoteBinPath != "" {
		unrealsyncBinaryPath = r.settings.remoteBinPath
	} else if unrealsyncBinaryPath == "" || unrealsyncVersion != VERSION {
		unrealsyncBinaryPathForHost := unrealsyncDir + "/unrealsync-" + ostype + "-" + osarch
		r.copyUnrealsyncBinaries(unrealsyncBinaryPathForHost)
		unrealsyncBinaryPath = r.settings.dir + "/.unrealsync/unrealsync"
	}

	cmd, stdin, stdout, stderr = r.launchUnrealsyncAt(unrealsyncBinaryPath)

	stream := make(chan BufBlocker)
	// receive from singlestdinwriter (stream) and send into ssh stdin
	go singleStdinWriter(stream, stdin, r.errorCh, r.stopCh)
	// read log and send into ssh stdin via singlestdinwriter (stream)
	// stops if stopChan closes and closes stream
	go doSendChanges(stream, r.settings.host, r.stopCh, r.errorCh)
	// read ssh stdout and send into ssh stdin via singlestdinwriter (stream)
	go pingReplyThread(stdout, r.settings.host, stream, r.errorCh)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stderr.Read(buf)
			if err != nil {
				break
			}
			progressWithout(string(buf[0:n]))
		}
	}()

	err := <-r.errorCh
	close(r.errorCh)
	close(r.stopCh)
	panic(err)
}

func (r *Client) launchUnrealsyncAt(unrealsyncBinaryPath string) (*exec.Cmd, io.WriteCloser, io.ReadCloser, io.ReadCloser) {
	progressLn("Launching unrealsync at " + r.settings.host + "...")

	args := sshOptions(r.settings)
	// TODO: escaping
	flags := "--server --hostname=" + r.settings.host
	if isDebug {
		flags += " --debug"
	}
	for dir, _ := range r.settings.excludes {
		flags += " --exclude " + dir
	}

	unrealsyncLaunchCmd := unrealsyncBinaryPath + " " + flags + " " + r.settings.dir
	if r.settings.sudouser != "" {
		unrealsyncLaunchCmd = "sudo -u " + r.settings.sudouser + " " + unrealsyncLaunchCmd
	}
	args = append(args, r.settings.host, unrealsyncLaunchCmd)

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

	stderr, err := cmd.StderrPipe()
	if err != nil {
		fatalLn("Cannot get stdin pipe: ", err.Error())
	}

	if err = cmd.Start(); err != nil {
		panic("Cannot start command ssh " + strings.Join(args, " ") + ": " + err.Error())
	}
	return cmd, stdin, stdout, stderr
}

func (r *Client) createDirectoriesAt() (ostype, osarch, unrealsyncBinaryPath, unrealsyncVersion string) {
	progressLn("Creating directories at " + r.settings.host + "...")

	args := sshOptions(r.settings)
	// TODO: escaping
	dir := r.settings.dir + "/.unrealsync"
	args = append(args, r.settings.host, "if [ ! -d "+dir+" ]; then mkdir -p "+dir+"; fi;"+
		"rm -f "+dir+"/unrealsync &&"+
		"uname && uname -m && if ! which unrealsync 2>/dev/null ; then echo 'no-binary'; echo 'no-version';"+
		"else unrealsync --version 2>/dev/null ; echo 'no-version' ; fi")

	output := execOrPanic("ssh", args, r.stopCh)
	uname := strings.Split(strings.TrimSpace(output), "\n")

	return strings.ToLower(uname[0]), uname[1], uname[2], uname[3]
}

func singleStdinWriter(stream chan BufBlocker, stdin io.WriteCloser, errorCh chan error, stopCh chan bool) {
	var bufBlocker BufBlocker
	for {
		select {
		case bufBlocker = <-stream:
		case <-stopCh:
			break
		}
		_, err := stdin.Write(bufBlocker.buf)
		bufBlocker.sent <- true
		if err != nil {
			sendErrorNonBlocking(errorCh, err)
			break
		}
	}
}

func pingReplyThread(stdout io.ReadCloser, hostname string, stream chan BufBlocker, errorCh chan error) {
	bufBlocker := BufBlocker{buf: make([]byte, 20), sent: make(chan bool)}
	bufBlocker.buf = []byte(ACTION_PONG + fmt.Sprintf("%10d", 0))
	buf := make([]byte, 10)
	for {
		read_bytes, err := io.ReadFull(stdout, buf)
		if err != nil {
			sendErrorNonBlocking(errorCh, errors.New("Could not read from server:"+hostname+" err:"+err.Error()))
			break
		}
		debugLn("Read ", read_bytes, " from ", hostname, " ", buf)
		stream <- bufBlocker
		<-bufBlocker.sent
	}
}

func (r *Client) notifySendQueueSize(sendQueueSize int64) (err error) {
	if r.settings.sendQueueSizeLimit != 0 && sendQueueSize > r.settings.sendQueueSizeLimit {
		err = errors.New("SendQueueSize limit exceeded for " + r.settings.host)
		warningLn(err)
		select {
		case r.errorCh <- err:
		default:
		}
	}
	return
}
