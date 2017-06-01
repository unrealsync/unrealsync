package main

import (
	"flag"
	"fmt"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	REPO_DIR           = ".unrealsync/"
	REPO_CLIENT_CONFIG = REPO_DIR + "client_config"
	REPO_TMP           = REPO_DIR + "tmp/"
	REPO_LOG_FILENAME  = REPO_DIR + "out.log"
	REPO_PID           = REPO_DIR + "pid"
	REPO_PID_SERVER    = REPO_DIR + "pid_server"

	DIFF_SEP = "\n------------\n"

	// all actions must be 10 symbols length
	ACTION_PING       = "PING      "
	ACTION_PONG       = "PONG      "
	ACTION_DIFF       = "DIFF      "
	ACTION_BIG_INIT   = "BIGINIT   "
	ACTION_BIG_RCV    = "BIGRCV    "
	ACTION_BIG_COMMIT = "BIGCOMMIT "
	ACTION_BIG_ABORT  = "BIGABORT  "

	MAX_DIFF_SIZE           = 2 * 1024 * 1204
	DEFAULT_CONNECT_TIMEOUT = 10
	RETRY_INTERVAL          = 10 * time.Second
	SERVER_ALIVE_INTERVAL   = 3
	SERVER_ALIVE_COUNT_MAX  = 4

	PING_INTERVAL          = time.Minute
	DIR_AGGREGATE_INTERVAL = 400 * time.Millisecond
	LOCAL_WATCHER_READY    = "Initialized"
)

type MultipleStringFlag []string

func (r *MultipleStringFlag) String() string {
	return fmt.Sprint([]string(*r))
}
func (r *MultipleStringFlag) Set(value string) (err error) {
	*r = append(*r, value)
	return
}

var (
	sourceDir        string
	unrealsyncDir    string
	rcvchan          = make(chan bool)
	isServer         = false
	isDebug          = false
	hostname         = ""
	excludesFlag     MultipleStringFlag
	forceServersFlag = ""
)

func init() {
	flag.BoolVar(&isDebug, "debug", false, "Turn on debugging information")
	flag.BoolVar(&isServer, "server", false, "Internal parameter used on remote side")
	flag.StringVar(&hostname, "hostname", "", "Internal parameter used on remote side")
	flag.Var(&excludesFlag, "excludes", "Internal parameter used on remote side")
	flag.StringVar(&forceServersFlag, "servers", "", "Perform sync only for specified servers")
}

func initUnrealsyncDir() string {
	var err error
	dir := path.Dir(os.Args[0])
	if dir == "." {
		currentUser, err := user.Current()
		if err != nil {
			fatalLn("Could not obtain current user: ", err)
		}
		for _, part := range filepath.SplitList(os.Getenv("PATH")) {
			filePathProbablySymlink := strings.Replace(part+"/"+os.Args[0], "~", currentUser.HomeDir, 1)
			file, err := filepath.EvalSymlinks(filePathProbablySymlink)
			if err == nil {
				dir = path.Dir(file)
				break
			}
		}
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		fatalLn("Cannot determine unrealsync binary location: " + err.Error())
	}
	return dir
}

func writePidFileAndKillPrevious(pid_filename string) {
	if pid_file, err := os.Open(pid_filename); err == nil {
		var pid int
		_, err = fmt.Fscanf(pid_file, "%d", &pid)
		if err != nil {
			fatalLn("Cannot read pid from " + pid_filename + ": " + err.Error())
		}

		proc, err := os.FindProcess(pid)
		if err == nil {
			proc.Kill()
		}

		pid_file.Close()
	}

	pid_file, err := os.OpenFile(pid_filename, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0666)
	defer pid_file.Close()
	if err != nil {
		fatalLn("Cannot open " + pid_filename + " for writing: " + err.Error())
	}

	_, err = fmt.Fprint(pid_file, os.Getpid())
	if err != nil {
		fatalLn("Cannot write current pid to " + pid_filename + ": " + err.Error())
	}
}

func main() {
	var err error

	flag.Parse()
	args := flag.Args()

	if len(args) > 1 {
		fmt.Fprintln(os.Stderr, "Usage: unrealsync [<flags>] [<dir>]")
		fmt.Fprintln(os.Stderr, "Current args:", args)
		flag.PrintDefaults()
		os.Exit(2)
	} else if len(args) == 1 {
		if err := os.Chdir(args[0]); err != nil {
			fatalLn("Cannot chdir to ", args[0])
		}
	}

	unrealsyncDir = initUnrealsyncDir()
	debugLn("Unrealsync is in directory ", unrealsyncDir)

	sourceDir, err = os.Getwd()
	if err != nil {
		fatalLn("Cannot get current directory: " + err.Error())
	}
	if isServer {
		progressLn("Unrealsync server starting at ", sourceDir)
	} else {
		progressLn("Unrealsync starting from ", sourceDir)
	}

	os.RemoveAll(REPO_TMP)

	for _, dir := range []string{REPO_DIR, REPO_TMP} {
		_, err = os.Stat(dir)
		if err != nil {
			err = os.Mkdir(dir, 0777)
			if err != nil {
				fatalLn("Cannot create " + dir + ": " + err.Error())
			}
		}
	}

	var pid_filename string
	if isServer {
		pid_filename = REPO_PID_SERVER
	} else {
		pid_filename = REPO_PID
	}
	writePidFileAndKillPrevious(pid_filename)

	initializeLogs()

	if isServer {
		doServer()
	} else {
		doClient()
	}
}
