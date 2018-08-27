package main

import "strings"

type Repository struct {
	stats    map[string]map[string]*UnrealStat
	Excludes map[string]bool
}

func NewRepository(excludes map[string]bool) *Repository {
	return &Repository{
		stats:    make(map[string]map[string]*UnrealStat),
		Excludes: excludes,
	}
}

func (r *Repository) HasDir(dir string) bool {
	_, ok := r.stats[dir]
	return ok
}

func (r *Repository) AddDir(dir string) {
	r.stats[dir] = make(map[string]*UnrealStat)
}

func (r *Repository) GetDirStat(dir string) map[string]*UnrealStat {
	stat, ok := r.stats[dir]
	if !ok {
		return nil
	}

	return stat
}

func (r *Repository) SetDirStat(dir string, stat map[string]*UnrealStat) {
	r.stats[dir] = stat
}

func (r *Repository) AddFileToDir(dir, file string, stat *UnrealStat) {
	r.stats[dir][file] = stat
}

func (r *Repository) IsPathExcluded(path string) bool {
	if strings.HasPrefix(path, ".unrealsync") { // todo: don't we have it in excludes always?
		return true
	}
	for exclude := range r.Excludes {
		if strings.HasPrefix(path, exclude) {
			return true
		}
	}
	return false
}
