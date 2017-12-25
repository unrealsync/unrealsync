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
	version = "1.0.3"

	// Files stored in repo folder
	defaultRepoDir        = ".unrealsync/"
	repoConfigFilename    = defaultRepoDir + "client_config"
	repoTmp               = "tmp"
	repoLogFilename       = "out.log"
	repoPidFilename       = "pid"
	repoPidServerFilename = "pid_server"

	diffSep = "\n------------\n"

	// all actions must be 10 symbols length
	actionPing      = "PING      "
	actionPong      = "PONG      "
	actionDiff      = "DIFF      "
	actionBigInit   = "BIGINIT   "
	actionBigRcv    = "BIGRCV    "
	actionBigCommit = "BIGCOMMIT "
	actionBigAbort  = "BIGABORT  "

	maxDiffSize           = 2 * 1024 * 1204
	defaultConnectTimeout = 10
	retryInterval         = 10 * time.Second
	serverAliveInterval   = 3
	serverAliveCountMax   = 4

	pingInterval         = time.Minute
	dirAggregateInterval = 400 * time.Millisecond
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
	repoPath         string
	sudoUser         string
	rcvchan          = make(chan bool)
	isServer         = false
	isDebug          = false
	isVersion        = false
	hostname         = ""
	excludesFlag     MultipleStringFlag
	forceServersFlag = ""
	hashCheck        = false
)

func init() {
	flag.BoolVar(&isVersion, "version", false, "Show version")
	flag.BoolVar(&isDebug, "debug", false, "Turn on debugging information")
	flag.BoolVar(&isServer, "server", false, "(internal) Internal parameter used on remote side")
	flag.StringVar(&hostname, "hostname", "", "(internal) Internal parameter used on remote side")
	flag.Var(&excludesFlag, "exclude", "(internal) Internal parameter used on remote side")
	flag.StringVar(&forceServersFlag, "servers", "", "Perform sync only for specified servers")
	flag.StringVar(&repoPath, "repo-path", "", "Store logs and pid file in specified folder")
	flag.StringVar(&sudoUser, "sudo-user", "", "Use this user to store files on the remote side")
	flag.BoolVar(&hashCheck, "hash-check", false, "Use md5 hashing to check if file content changed before syncing it")
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
	servers := make(map[string]Settings)

	flag.Parse()
	args := flag.Args()

	if isVersion {
		fmt.Println(version)
		os.Exit(0)
	} else if len(args) > 0 {
		var err error
		if len(repoPath) != 0 {
			repoPath, err = filepath.Abs(repoPath)
			if err != nil {
				fatalLn(err)
			}
		} else {
			repoPath = defaultRepoDir
		}
		if err := os.Chdir(args[0]); err != nil {
			fatalLn("Cannot chdir to ", args[0])
		}
		for i := 1; i < len(args); i++ {
			parts := strings.Split(args[i], ":")
			if len(parts) != 2 {
				fatalLn("bad host:dir specification:" + args[i])
			}
			var serverSettings Settings
			if hostUserParts := strings.Split(parts[0], "@"); len(hostUserParts) == 2 {
				serverSettings = Settings{username: hostUserParts[0], host: hostUserParts[1], dir: parts[1]}
			} else {
				serverSettings = Settings{host: parts[0], dir: parts[1]}
			}
			if len(sudoUser) > 0 {
				serverSettings.sudouser = sudoUser
			}
			servers[parts[0]] = serverSettings
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

	tmpFolder := path.Join(repoPath, repoTmp)
	os.RemoveAll(tmpFolder)

	for _, dir := range []string{repoPath, tmpFolder} {
		_, err = os.Stat(dir)
		if err != nil {
			err = os.Mkdir(dir, 0777)
			if err != nil {
				fatalLn("Cannot create " + dir + ": " + err.Error())
			}
		}
	}

	var pidFilename string
	if isServer {
		pidFilename = path.Join(repoPath, repoPidServerFilename)
	} else {
		pidFilename = path.Join(repoPath, repoPidFilename)
	}
	writePidFileAndKillPrevious(pidFilename)

	initializeLogs()

	if isServer {
		doServer()
	} else {
		doClient(servers)
	}
}
