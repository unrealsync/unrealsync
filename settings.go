package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/bo0rsh201/unrealsync/list"
	ini "github.com/glacjay/goini"
)

const GENERAL_SECTION = "general_settings"

type Settings struct {
	excludes map[string]bool
	username string
	sudouser string
	host     string
	port     int

	dir           string
	remoteBinPath string
	os            string

	compression bool
}

func parseServerSettings(section string, serverSettings map[string]string, excludes map[string]bool) Settings {

	var (
		port int
		err  error
	)

	if serverSettings["port"] != "" {
		port, err = strconv.Atoi(serverSettings["port"])
		if err != nil {
			fatalLn("Cannot parse 'port' property in [" + section + "] section of " + REPO_CLIENT_CONFIG + ": " + err.Error())
		}
	}

	localExcludes := make(map[string]bool)

	for key, value := range excludes {
		localExcludes[key] = value
	}

	if serverSettings["exclude"] != "" {
		for key, value := range parseExcludes(serverSettings["exclude"]) {
			localExcludes[key] = value
		}
	}

	host, ok := serverSettings["host"]
	if !ok {
		host = section
	}

	compression := (serverSettings["compression"] != "false")

	return Settings{
		localExcludes,
		serverSettings["username"],
		serverSettings["sudouser"],
		host,
		port,
		serverSettings["dir"],
		serverSettings["remote-bin-path"],
		serverSettings["os"],
		compression,
	}

}

func parseExcludes(excl string) map[string]bool {
	result := make(map[string]bool)

	for _, filename := range strings.Split(excl, "|") {
		result[filename] = true
	}

	return result
}

func parseConfig() (servers map[string]Settings, excludes map[string]bool) {
	servers = make(map[string]Settings)
	excludes = make(map[string]bool)
	dict, err := ini.Load(REPO_CLIENT_CONFIG)

	if err != nil {
		fatalLn("Cannot parse client_config file: ", err)
	}

	general, ok := dict[GENERAL_SECTION]
	if !ok {
		fatalLn("Section " + GENERAL_SECTION + " of config file " + REPO_CLIENT_CONFIG + " is empty")
	}

	excludes[".unrealsync"] = true
	if general["exclude"] != "" {
		for key, value := range parseExcludes(general["exclude"]) {
			excludes[key] = value
		}
	}

	forceServers := general["servers"]
	if forceServersFlag != "" {
		forceServers = forceServersFlag
	}

	delete(dict, GENERAL_SECTION)

	for key, serverSettings := range dict {
		if key == "" {
			if len(serverSettings) > 0 {
				progressLn("You should not have top-level settings in " + REPO_CLIENT_CONFIG)
			}
			continue
		}

		if _, ok := serverSettings["disabled"]; ok {
			progressLn("Skipping [" + key + "] as disabled")
			continue
		}

		for generalKey, generalValue := range general {
			if serverSettings[generalKey] == "" {
				serverSettings[generalKey] = generalValue
			}
		}
		var keys []string
		keys, err = list.Expand(key)
		if err != nil {
			fatalLn(fmt.Sprintf(
				"Server name pattern '%s' parse error [config]: %s", key, err,
			))
		}
		for _, k := range keys {
			if forceServers != "" {
				var res bool
				res, err = list.Glob(forceServers, k)
				if err != nil {
					fatalLn(fmt.Sprintf(
						"Server name pattern '%s' parse error [override]: %s", key, err,
					))
				}
				if !res {
					continue
				}
			}
			servers[k] = parseServerSettings(k, serverSettings, excludes)
		}
	}
	return
}
