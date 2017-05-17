package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type UnrealStat struct {
	isDir  bool
	isLink bool
	mode   int16
	mtime  int64
	size   int64
}

func (p UnrealStat) Serialize() (res string) {
	res = ""
	if p.isDir {
		res += "dir "
	}
	if p.isLink {
		res += "symlink "
	}

	res += fmt.Sprintf("mode=%o mtime=%d size=%v", p.mode, p.mtime, p.size)
	return
}

func StatsEqual(orig os.FileInfo, repo UnrealStat) bool {
	if repo.isDir != orig.IsDir() {
		debugLn(orig.Name(), " is not dir")
		return false
	}

	if repo.isLink != (orig.Mode()&os.ModeSymlink == os.ModeSymlink) {
		debugLn(orig.Name(), " symlinks different")
		return false
	}

	// TODO: better handle symlinks :)
	// do not check filemode for symlinks because we cannot chmod them either
	if !repo.isLink && (repo.mode&0777) != int16(uint32(orig.Mode())&0777) {
		debugLn(orig.Name(), " modes different")
		return false
	}

	// you cannot set mtime for a symlink and we do not set mtime for directories
	if !repo.isLink && !repo.isDir && repo.mtime != orig.ModTime().Unix() {
		debugLn(orig.Name(), " modification time different")
		return false
	}

	if !repo.isDir && repo.size != orig.Size() {
		debugLn(orig.Name(), " size different")
		return false
	}

	return true
}

func UnrealStatUnserialize(input string) (result UnrealStat) {
	for _, part := range strings.Split(input, " ") {
		if part == "dir" {
			result.isDir = true
		} else if part == "symlink" {
			result.isLink = true
		} else if strings.HasPrefix(part, "mode=") {
			tmp, _ := strconv.ParseInt(part[len("mode="):], 8, 16)
			result.mode = int16(tmp)
		} else if strings.HasPrefix(part, "mtime=") {
			result.mtime, _ = strconv.ParseInt(part[len("mtime="):], 10, 64)
		} else if strings.HasPrefix(part, "size=") {
			result.size, _ = strconv.ParseInt(part[len("size="):], 10, 64)
		}
	}

	return
}

func UnrealStatFromStat(info os.FileInfo) UnrealStat {
	return UnrealStat{
		info.IsDir(),
		(info.Mode()&os.ModeSymlink == os.ModeSymlink),
		int16(uint32(info.Mode()) & 0777),
		info.ModTime().Unix(),
		info.Size(),
	}
}
