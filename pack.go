package gitgo

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"strconv"
	"strings"
)

type packObject struct {
	Name        SHA
	Offset      int
	Data        []byte
	_type       packObjectType
	PatchedData []byte

	Size int // the uncompressed size

	SizeInPackfile int // the compressed size

	// only used for OBJ_OFS_DELTA
	negativeOffset int
	BaseObjectName SHA
	BaseObjectType packObjectType
	baseOffset     int
	Depth          int

	err error // was an error encountered while processing this object?
}

func (p *packObject) Type() string {
	return p._type.String()
}

// normalize returns a GitObject equivalent to the packObject.
// packObject satisfies the GitObject interface, but if the pack
// object type is a commit, tree, or blob, it will return a Commit,
// Tree, or Blob struct instead of the packObject
func (p *packObject) normalize(basedir string) (GitObject, error) {
	switch p._type {
	case OBJ_COMMIT:
		return p.Commit(basedir)
	case OBJ_TREE:
		return p.Tree(basedir)
	case OBJ_BLOB:
		return p.Blob(basedir)
	default:
		return p, nil
	}
}

// Commit returns a Commit struct for the packObject.
func (p *packObject) Commit(basedir string) (Commit, error) {
	if p._type != OBJ_COMMIT {
		return Commit{}, fmt.Errorf("pack object is not a commit: %s", p.Type())
	}
	if p.PatchedData == nil {
		p.PatchedData = p.Data
	}

	commit, err := parseCommit(bytes.NewReader(p.PatchedData), strconv.Itoa(p.Size), p.Name)
	return commit, err
}

// Tree returns a Tree struct for the packObject.
func (p *packObject) Tree(basedir string) (Tree, error) {
	if p._type != OBJ_TREE {
		return Tree{}, fmt.Errorf("pack object is not a tree: %s", p.Type())
	}
	if p.PatchedData == nil {
		p.PatchedData = p.Data
	}

	tree, err := parseTree(bytes.NewReader(p.PatchedData), strconv.Itoa(p.Size), basedir)
	return tree, err
}

// Blob returns a Blob struct for the packObject.
func (p *packObject) Blob(basedir string) (Blob, error) {
	if p._type != OBJ_BLOB {
		return Blob{}, fmt.Errorf("pack object is not a blob: %s", p.Type())
	}
	if p.PatchedData == nil {
		p.PatchedData = p.Data
	}

	blob, err := parseBlob(bytes.NewReader(p.PatchedData), basedir)
	return blob, err
}

func (p *packObject) Patch(dict map[SHA]*packObject) error {
	if p.Name == "1d833eb5b6c5369c0cb7a4a3e20ded237490145f" {
		defer func() {
		}()
	}
	if len(p.PatchedData) != 0 {
		return nil
	}
	if p._type < OBJ_OFS_DELTA {
		if p.Data == nil {
			return fmt.Errorf("base object data is nil")
		}
		p.PatchedData = p.Data
		p.BaseObjectType = p._type
		return nil
	}

	if p._type >= OBJ_OFS_DELTA {
		base, ok := dict[p.BaseObjectName]
		if !ok {
			return fmt.Errorf("base object not in dictionary: %s", p.BaseObjectName)
		}
		err := base.Patch(dict)
		if err != nil {
			return err
		}

		// At the time patchDelta is called, we know that the base.PatchedData is non-nil
		patched, err := patchDelta(bytes.NewReader(base.PatchedData), bytes.NewReader(p.Data))
		if err != nil {
			return err
		}

		p.PatchedData, err = ioutil.ReadAll(patched)
		if err != nil {
			return err
		}

		p.BaseObjectType = base.BaseObjectType
		p.Depth += base.Depth
	}
	return nil
}

func (p *packObject) PatchedType() packObjectType {
	if p._type < OBJ_OFS_DELTA {
		return p._type
	}
	return p.BaseObjectType
}

//go:generate stringer -type=packObjectType
type packObjectType uint8

const (
	_ packObjectType = iota
	OBJ_COMMIT
	OBJ_TREE
	OBJ_BLOB
	OBJ_TAG
	_
	OBJ_OFS_DELTA
	OBJ_REF_DELTA
)

func searchPacks(object SHA, basedir string) (*packObject, error) {
	packs, err := listPackfiles(basedir)
	if err != nil {
		return nil, err
	}
	return objInPacks(packs, object, basedir)
}

func listPackfiles(basedir string) ([]SHA, error) {
	files, err := ioutil.ReadDir(path.Join(basedir, "objects", "pack"))
	if err != nil {
		return nil, err
	}
	packfileNames := []SHA{}
	for _, file := range files {
		base := strings.TrimSuffix(file.Name(), ".pack")
		if base == file.Name() {
			// this wasn't a packfile
			continue
		}
		packfileNames = append(packfileNames, SHA(base))
	}
	return packfileNames, nil
}

func objInPacks(packs []SHA, object SHA, basedir string) (*packObject, error) {
	for _, name := range packs {
		pf, err := os.Open(path.Join(basedir, "objects", "pack", string(name)+".pack"))
		if err != nil {
			return nil, err
		}
		defer pf.Close()
		inf, err := os.Open(path.Join(basedir, "objects", "pack", string(name)+".idx"))
		if err != nil {
			return nil, err
		}
		defer inf.Close()

		objs, err := VerifyPack(pf, inf)
		if err != nil {
			return nil, err
		}

		for _, obj := range objs {
			if strings.HasPrefix(string(obj.Name), string(object)) {
				return obj, nil
			}
		}
	}
	return nil, fmt.Errorf("object not in any packfiles: %s", object)
}
