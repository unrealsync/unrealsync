package main

import (
	"flag"
	"fmt"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	version = "1.0.11"

	// Files stored in repo folder
	defaultRepoDir        = ".unrealsync/"
	repoConfigFilename    = defaultRepoDir + "client_config"
	repoTmp               = "tmp"
	repoLogFilename       = "out.log"
	repoPidFilename       = "pid"
	repoPidServerFilename = "pid_server"

	diffSep = "\n------------\n"

	// all actions must be 10 symbols length
	actionPing       = "PING      "
	actionPong       = "PONG      "
	actionDiff       = "DIFF      "
	actionBigInit    = "BIGINIT   "
	actionBigRcv     = "BIGRCV    "
	actionBigCommit  = "BIGCOMMIT "
	actionBigAbort   = "BIGABORT  "
	actionStopServer = "STOPSERVER"

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
	remoteBinPath    string
	rcvchan          = make(chan bool)
	isServer         = false
	isDebug          = false
	isVersion        = false
	isHelp           = false
	hostname         = ""
	excludesFlag     MultipleStringFlag
	forceServersFlag = ""
	hashCheck        = false
)

func init() {
	flag.BoolVar(&isHelp, "help", false, "Show help and exit")
	flag.BoolVar(&isVersion, "version", false, "Show version and exit")
	flag.BoolVar(&isDebug, "debug", false, "Turn on debugging information")
	flag.Var(&excludesFlag, "exclude", "Exclude specified path from sync. Also used as internal parameter on the remote side")
	flag.StringVar(&forceServersFlag, "servers", "", "Perform sync only for specified servers")
	flag.StringVar(&repoPath, "repo-path", "", "Store logs and pid file in specified folder")
	flag.StringVar(&sudoUser, "sudo-user", "", "Use this user to store files on the remote side")
	flag.StringVar(&remoteBinPath, "remote-bin-path", "", "Specify the unrealsync path to run on remote side")
	flag.BoolVar(&hashCheck, "hash-check", false, "Use md5 hashing to check if file content changed before syncing it")
	// keep internal parameters to be the last; todo: find something to replace flag and hide internal from .PrintDefault()'s output
	flag.BoolVar(&isServer, "server", false, "(internal) Internal parameter used on remote side")
	flag.StringVar(&hostname, "hostname", "", "(internal) Internal parameter used on remote side")
}

func printHelp() {
	fmt.Println("unrealsync is utility that can perform synchronization between several servers")
	fmt.Println()
	fmt.Println("usage: unrealsync [<options>] <local directory> [<server>:<remote directory>] [<user>@<server>:<remote directory>]")
	fmt.Println("                  <directory> - directory to sync")
	fmt.Println()
	fmt.Println("You may specify as many remote directories as you need")
	fmt.Println()
	fmt.Println("Available options are:")
	flag.PrintDefaults()
	fmt.Println()
	fmt.Println("Unrealsync also supports per-folder config files. To find out more read Config section at https://github.com/unrealsync/unrealsync")

	// todo make better help
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

func writePidFileAndKillPrevious(pidFilename string) {
	if pidFile, err := os.Open(pidFilename); err == nil {
		var pid int
		_, err = fmt.Fscanf(pidFile, "%d", &pid)
		if err != nil {
			fatalLn("Cannot read pid from " + pidFilename + ": " + err.Error())
		}

		proc, err := os.FindProcess(pid)
		if err == nil {
			proc.Signal(syscall.SIGUSR1)
			// give some time for process to stop normally
			time.Sleep(250 * time.Millisecond)
			// need this for back-compatibility. Need to drop with major version inc
			proc.Kill()
		}

		pidFile.Close()
	}

	pidFile, err := os.OpenFile(pidFilename, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0666)
	defer pidFile.Close()
	if err != nil {
		fatalLn("Cannot open " + pidFilename + " for writing: " + err.Error())
	}

	_, err = fmt.Fprint(pidFile, os.Getpid())
	if err != nil {
		fatalLn("Cannot write current pid to " + pidFilename + ": " + err.Error())
	}
}

func main() {
	var err error
	var globalExcludes map[string]bool
	servers := make(map[string]Settings)

	flag.Parse()
	args := flag.Args()

	if isHelp {
		printHelp()
		os.Exit(0)
	} else if isVersion {
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
		if len(excludesFlag) > 0 {
			globalExcludes = make(map[string]bool)
			for _, exclude := range excludesFlag {
				globalExcludes[exclude] = true
			}
			globalExcludes[".unrealsync"] = true
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
			if len(remoteBinPath) > 0 {
				serverSettings.remoteBinPath = remoteBinPath
			}
			if len(globalExcludes) > 0 {
				serverSettings.excludes = make(map[string]bool)
				for k, v := range globalExcludes {
					serverSettings.excludes[k] = v
				}
			}
			servers[serverSettings.host] = serverSettings
		}
	} else {
		fmt.Fprintf(os.Stderr, "ERR: You should specify directory to sync\nTry unrealsync --help for more information\n")
		os.Exit(123)
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
			err = os.Mkdir(dir, 0755)
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
		doClient(servers, globalExcludes)
	}
}
