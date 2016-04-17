package coreunix

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	gopath "path"

	ds "github.com/ipfs/go-ipfs/Godeps/_workspace/src/github.com/ipfs/go-datastore"
	syncds "github.com/ipfs/go-ipfs/Godeps/_workspace/src/github.com/ipfs/go-datastore/sync"
	bstore "github.com/ipfs/go-ipfs/blocks/blockstore"
	key "github.com/ipfs/go-ipfs/blocks/key"
	bserv "github.com/ipfs/go-ipfs/blockservice"
	"github.com/ipfs/go-ipfs/exchange/offline"
	importer "github.com/ipfs/go-ipfs/importer"
	"github.com/ipfs/go-ipfs/importer/chunk"
	mfs "github.com/ipfs/go-ipfs/mfs"
	"github.com/ipfs/go-ipfs/pin"
	context "gx/ipfs/QmZy2y8t9zQH2a1b8q2ZSLKp17ATuJoCNxxyMFG5qFExpt/go-net/context"

	bs "github.com/ipfs/go-ipfs/blocks/blockstore"
	"github.com/ipfs/go-ipfs/commands/files"
	core "github.com/ipfs/go-ipfs/core"
	dag "github.com/ipfs/go-ipfs/merkledag"
	unixfs "github.com/ipfs/go-ipfs/unixfs"
	logging "gx/ipfs/Qmazh5oNUVsDZTs2g59rq8aYQqwpss8tcUWQzor5sCCEuH/go-log"
)

var log = logging.Logger("coreunix")

var folderData = unixfs.FolderPBData()

// how many bytes of progress to wait before sending a progress update message
const progressReaderIncrement = 1024 * 256

type Link struct {
	Name, Hash string
	Size       uint64
}

type Object struct {
	Hash  string
	Links []Link
}

type hiddenFileError struct {
	fileName string
}

func (e *hiddenFileError) Error() string {
	return fmt.Sprintf("%s is a hidden file", e.fileName)
}

type ignoreFileError struct {
	fileName string
}

func (e *ignoreFileError) Error() string {
	return fmt.Sprintf("%s is an ignored file", e.fileName)
}

type AddedObject struct {
	Name  string
	Hash  string `json:",omitempty"`
	Bytes int64  `json:",omitempty"`
}

func NewAdder(ctx context.Context, n *core.IpfsNode, out chan interface{}) (*Adder, error) {
	mr, err := mfs.NewRoot(ctx, n.DAG, newDirNode(), nil)
	if err != nil {
		return nil, err
	}

	return &Adder{
		mr:       mr,
		ctx:      ctx,
		node:     n,
		out:      out,
		Progress: false,
		Hidden:   true,
		Pin:      true,
		Trickle:  false,
		Wrap:     false,
		Chunker:  "",
	}, nil
}

// Internal structure for holding the switches passed to the `add` call
type Adder struct {
	ctx      context.Context
	node     *core.IpfsNode
	out      chan interface{}
	Progress bool
	Hidden   bool
	Pin      bool
	Trickle  bool
	Silent   bool
	Wrap     bool
	Chunker  string
	root     *dag.Node
	mr       *mfs.Root
	unlocker bs.Unlocker
	tempRoot key.Key
}

// Perform the actual add & pin locally, outputting results to reader
func (adder Adder) add(reader io.Reader) (*dag.Node, error) {
	chnk, err := chunk.FromString(reader, adder.Chunker)
	if err != nil {
		return nil, err
	}

	if adder.Trickle {
		return importer.BuildTrickleDagFromReader(
			adder.node.DAG,
			chnk,
		)
	}
	return importer.BuildDagFromReader(
		adder.node.DAG,
		chnk,
	)
}

func (adder *Adder) RootNode() (*dag.Node, error) {
	// for memoizing
	if adder.root != nil {
		return adder.root, nil
	}

	root, err := adder.mr.GetValue().GetNode()
	if err != nil {
		return nil, err
	}

	// if not wrapping, AND one root file, use that hash as root.
	if !adder.Wrap && len(root.Links) == 1 {
		root, err = root.Links[0].GetNode(adder.ctx, adder.node.DAG)
		if err != nil {
			return nil, err
		}
	}

	adder.root = root
	return root, err
}

func (adder *Adder) PinRoot() error {
	root, err := adder.RootNode()
	if err != nil {
		return err
	}
	if !adder.Pin {
		return nil
	}

	rnk, err := adder.node.DAG.Add(root)
	if err != nil {
		return err
	}

	if adder.tempRoot != "" {
		err := adder.node.Pinning.Unpin(adder.ctx, adder.tempRoot, true)
		if err != nil {
			return err
		}
		adder.tempRoot = rnk
	}

	adder.node.Pinning.PinWithMode(rnk, pin.Recursive)
	return adder.node.Pinning.Flush()
}

func (adder *Adder) Finalize() (*dag.Node, error) {
	// cant just call adder.RootNode() here as we need the name for printing
	root, err := adder.mr.GetValue().GetNode()
	if err != nil {
		return nil, err
	}

	var name string
	if !adder.Wrap {
		name = root.Links[0].Name
		child, err := root.Links[0].GetNode(adder.ctx, adder.node.DAG)
		if err != nil {
			return nil, err
		}
		root = child
	}

	err = adder.outputDirs(name, root)
	if err != nil {
		return nil, err
	}

	err = adder.mr.Close()
	if err != nil {
		return nil, err
	}

	return root, nil
}

func (adder *Adder) outputDirs(path string, nd *dag.Node) error {
	if !bytes.Equal(nd.Data, folderData) {
		return nil
	}

	for _, l := range nd.Links {
		child, err := l.GetNode(adder.ctx, adder.node.DAG)
		if err != nil {
			return err
		}

		err = adder.outputDirs(gopath.Join(path, l.Name), child)
		if err != nil {
			return err
		}
	}

	return outputDagnode(adder.out, path, nd)
}

// Add builds a merkledag from the a reader, pinning all objects to the local
// datastore. Returns a key representing the root node.
func Add(n *core.IpfsNode, r io.Reader) (string, error) {
	defer n.Blockstore.PinLock().Unlock()

	fileAdder, err := NewAdder(n.Context(), n, nil)
	if err != nil {
		return "", err
	}

	node, err := fileAdder.add(r)
	if err != nil {
		return "", err
	}
	k, err := node.Key()
	if err != nil {
		return "", err
	}

	return k.String(), nil
}

// AddR recursively adds files in |path|.
func AddR(n *core.IpfsNode, root string) (key string, err error) {
	n.Blockstore.PinLock().Unlock()

	stat, err := os.Lstat(root)
	if err != nil {
		return "", err
	}

	f, err := files.NewSerialFile(root, root, false, stat)
	if err != nil {
		return "", err
	}
	defer f.Close()

	fileAdder, err := NewAdder(n.Context(), n, nil)
	if err != nil {
		return "", err
	}

	err = fileAdder.addFile(f)
	if err != nil {
		return "", err
	}

	nd, err := fileAdder.Finalize()
	if err != nil {
		return "", err
	}

	k, err := nd.Key()
	if err != nil {
		return "", err
	}

	return k.String(), nil
}

// AddWrapped adds data from a reader, and wraps it with a directory object
// to preserve the filename.
// Returns the path of the added file ("<dir hash>/filename"), the DAG node of
// the directory, and and error if any.
func AddWrapped(n *core.IpfsNode, r io.Reader, filename string) (string, *dag.Node, error) {
	file := files.NewReaderFile(filename, filename, ioutil.NopCloser(r), nil)
	fileAdder, err := NewAdder(n.Context(), n, nil)
	if err != nil {
		return "", nil, err
	}
	fileAdder.Wrap = true

	defer n.Blockstore.PinLock().Unlock()

	err = fileAdder.addFile(file)
	if err != nil {
		return "", nil, err
	}

	dagnode, err := fileAdder.Finalize()
	if err != nil {
		return "", nil, err
	}

	k, err := dagnode.Key()
	if err != nil {
		return "", nil, err
	}

	return gopath.Join(k.String(), filename), dagnode, nil
}

func (adder *Adder) addNode(node *dag.Node, path string) error {
	// patch it into the root
	if path == "" {
		key, err := node.Key()
		if err != nil {
			return err
		}

		path = key.B58String()
	}

	dir := gopath.Dir(path)
	if dir != "." {
		if err := mfs.Mkdir(adder.mr, dir, true, false); err != nil {
			return err
		}
	}

	if err := mfs.PutNode(adder.mr, path, node); err != nil {
		return err
	}

	if !adder.Silent {
		return outputDagnode(adder.out, path, node)
	}
	return nil
}

// Add the given file while respecting the adder.
func (adder *Adder) AddFile(file files.File) error {
	adder.unlocker = adder.node.Blockstore.PinLock()
	defer func() {
		adder.unlocker.Unlock()
	}()

	return adder.addFile(file)
}

func (adder *Adder) addFile(file files.File) error {
	err := adder.maybePauseForGC()
	if err != nil {
		return err
	}

	if file.IsDirectory() {
		return adder.addDir(file)
	}

	// case for symlink
	if s, ok := file.(*files.Symlink); ok {
		sdata, err := unixfs.SymlinkData(s.Target)
		if err != nil {
			return err
		}

		dagnode := &dag.Node{Data: sdata}
		_, err = adder.node.DAG.Add(dagnode)
		if err != nil {
			return err
		}

		return adder.addNode(dagnode, s.FileName())
	}

	// case for regular file
	// if the progress flag was specified, wrap the file so that we can send
	// progress updates to the client (over the output channel)
	var reader io.Reader = file
	if adder.Progress {
		reader = &progressReader{file: file, out: adder.out}
	}

	dagnode, err := adder.add(reader)
	if err != nil {
		return err
	}

	// patch it into the root
	return adder.addNode(dagnode, file.FileName())
}

func (adder *Adder) addDir(dir files.File) error {
	log.Infof("adding directory: %s", dir.FileName())

	err := mfs.Mkdir(adder.mr, dir.FileName(), true, false)
	if err != nil {
		return err
	}

	for {
		file, err := dir.NextFile()
		if err != nil && err != io.EOF {
			return err
		}
		if file == nil {
			break
		}

		// Skip hidden files when adding recursively, unless Hidden is enabled.
		if files.IsHidden(file) && !adder.Hidden {
			log.Infof("%s is hidden, skipping", file.FileName())
			continue
		}
		err = adder.addFile(file)
		if err != nil {
			return err
		}
	}

	return nil
}

func (adder *Adder) maybePauseForGC() error {
	if adder.node.Blockstore.GCRequested() {
		err := adder.PinRoot()
		if err != nil {
			return err
		}

		adder.unlocker.Unlock()
		adder.unlocker = adder.node.Blockstore.PinLock()
	}
	return nil
}

// outputDagnode sends dagnode info over the output channel
func outputDagnode(out chan interface{}, name string, dn *dag.Node) error {
	if out == nil {
		return nil
	}

	o, err := getOutput(dn)
	if err != nil {
		return err
	}

	out <- &AddedObject{
		Hash: o.Hash,
		Name: name,
	}

	return nil
}

func NewMemoryDagService() dag.DAGService {
	// build mem-datastore for editor's intermediary nodes
	bs := bstore.NewBlockstore(syncds.MutexWrap(ds.NewMapDatastore()))
	bsrv := bserv.New(bs, offline.Exchange(bs))
	return dag.NewDAGService(bsrv)
}

// TODO: generalize this to more than unix-fs nodes.
func newDirNode() *dag.Node {
	return &dag.Node{Data: unixfs.FolderPBData()}
}

// from core/commands/object.go
func getOutput(dagnode *dag.Node) (*Object, error) {
	key, err := dagnode.Key()
	if err != nil {
		return nil, err
	}

	output := &Object{
		Hash:  key.B58String(),
		Links: make([]Link, len(dagnode.Links)),
	}

	for i, link := range dagnode.Links {
		output.Links[i] = Link{
			Name: link.Name,
			//Hash: link.Hash.B58String(),
			Size: link.Size,
		}
	}

	return output, nil
}

type progressReader struct {
	file         files.File
	out          chan interface{}
	bytes        int64
	lastProgress int64
}

func (i *progressReader) Read(p []byte) (int, error) {
	n, err := i.file.Read(p)

	i.bytes += int64(n)
	if i.bytes-i.lastProgress >= progressReaderIncrement || err == io.EOF {
		i.lastProgress = i.bytes
		i.out <- &AddedObject{
			Name:  i.file.FileName(),
			Bytes: i.bytes,
		}
	}

	return n, err
}
