[![Build Status](https://travis-ci.org/unrealsync/unrealsync.svg?branch=master)](https://travis-ci.org/unrealsync/unrealsync)

unrealsync
==========

Utility that can perform synchronization between several servers

Prerequisites
=============

 - Linux or Mac OS X (more OS support will be coming in some distant future)

All these tools present on both your machine *and remote server(s)*:

 - ssh
 - scp
 - rsync

Build
=====

Build unrealsync, using "go get" on both your machine and target server(s) or use pre-built binaries from releases section.

Usage
======

Run `unrealsync /path/on/local/machine server1.lan:/path/on/server1 user@server2.lan:/path/on/server2`. For more info run `unrealsync --help`

Unrealsync also supports per-folder config files. To use this feature create .unrealsync/client_config file in target directory and see Config section below.

NOTE: unrealsync will create .unrealsync directory in directory that is synced to store some temporary files.

Please also note that *unrealsync cannot run as daemon* yet, so you need to have a separate console window open in order for it to work.

After initial synchronization is done, you should be able to edit your files on your local machine and have them synchronized to remote servers with about 100-300 ms delay. Numbers can get higher if you have just made a large number of changes or have slow server connection.

Config
======

1. Create ".unrealsync" directory in the directory that you want to be synchronized
2. Create and edit ".unrealsync/client_config" file:

```
; this is a comment (it is actually parsed as .ini file)
; general settings section (must be present):
[general_settings]
exclude = excludes string ; (optional) excludes, in form "string1|string2|...|stringN"

; you can also put any settings that are common between all servers

; then, create one or more sections (put your name instead of "section")
[section]
dir = remote directory ; target directory on remote server

host = hostname ; (optional) hostname, if it is different from section name
port = port ; (optional) custom ssh port, if needed (default is taken from .ssh/config by ssh utility)
username = username ; (optional) custom ssh login, if needed
sudouser = sudouser ; (optional) custom user to launch unrealsync server under
remote-bin-path = remote path ; (optional) custom folder to search unrealsync (and notify utility for some os) binary in it
                              ; By default unrealsync will copy it's binaries in {dir}/.unrealsync folder on each launch
                              ; this option can be used if you want to copy them once in some folder and then just use pre-installed version
compression = false ; (optional) turn off ssh compression, if you have really fast connection (like 1 GBit/s) and unrealsync becomes CPU-bound
disabled = true ; (optional) temporarily disable the specified host and skip synchronization with it
send-queue-size-limit = 1000000000 ; (optional) limit send queue size in bytes. Changes are firstly put into log
                                   ; from which synchronisation to each server begins thus log may grow too much
                                   ; if synchronisation to server is slow. To prevent overgrowing log file unrealsync
                                   ; will restart sync
```

Config example
==============

```
[general_settings]
exclude = .git
dir = /home/yuriy/project/

[server1]

[server2]

[server3]
remote-bin-path = ~/bin/unrealsync ; since I'm not able to install unrealsync system-wide
```

Excluding .git folder may significantly increase performance.

