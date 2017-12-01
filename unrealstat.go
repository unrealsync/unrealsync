package main

import (
	"crypto/md5"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

type UnrealStat struct {
	name   string
	isDir  bool
	isLink bool
	mode   int16
	mtime  int64
	size   int64
	hash   string
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

func StatsEqual(newStat UnrealStat, oldStat UnrealStat) bool {
	if newStat.isDir != oldStat.isDir {
		debugLn(newStat.name, " is not dir")
		return false
	}

	if newStat.isLink != oldStat.isLink {
		debugLn(newStat.name, " symlinks different")
		return false
	}

	// TODO: better handle symlinks :)
	// do not check filemode for symlinks because we cannot chmod them either
	if !oldStat.isLink && (oldStat.mode&0777) != (newStat.mode&0777) {
		debugLn(newStat.name, " modes different")
		return false
	}

	if !oldStat.isDir && oldStat.size != newStat.size {
		debugLn(newStat.name, " size different")
		return false
	}

	// you cannot set mtime for a symlink and we do not set mtime for directories
	if !oldStat.isLink && !oldStat.isDir && oldStat.mtime != newStat.mtime && !hashEqual(newStat, oldStat) {
		debugLn(newStat.name, " modification time and hash are different")
		return false
	}

	return true
}

func hashEqual(newStat UnrealStat, oldStat UnrealStat) bool {
	if !hashCheck {
		return false
	}

	return oldStat.hash == newStat.Hash()
}

func (s *UnrealStat) Hash() string {
	if s.hash == "" {
		s.hash = computeMd5(s.name)
	}
	return s.hash
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

func UnrealStatFromStat(filePath string, info os.FileInfo) UnrealStat {
	return UnrealStat{
		filePath,
		info.IsDir(),
		info.Mode()&os.ModeSymlink == os.ModeSymlink,
		int16(uint32(info.Mode()) & 0777),
		info.ModTime().Unix(),
		info.Size(),
		"",
	}
}

func computeMd5(filePath string) string {
	file, err := os.Open(filePath)
	if err != nil {
		return ""
	}
	defer file.Close()

	hash := md5.New()
	if _, err := io.Copy(hash, file); err != nil {
		return ""
	}

	return string(hash.Sum(nil))
}
