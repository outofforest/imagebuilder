package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ridge/must"
	"github.com/ridge/parallel"
	"github.com/wojciech-malota-wojcik/imagebuilder/config"
	"github.com/wojciech-malota-wojcik/imagebuilder/infra/base"
	"github.com/wojciech-malota-wojcik/imagebuilder/infra/description"
	"github.com/wojciech-malota-wojcik/imagebuilder/infra/parser"
	"github.com/wojciech-malota-wojcik/imagebuilder/infra/storage"
	"github.com/wojciech-malota-wojcik/imagebuilder/infra/types"
	"github.com/wojciech-malota-wojcik/imagebuilder/lib/chroot"
	"github.com/wojciech-malota-wojcik/imagebuilder/lib/retry"
	"github.com/wojciech-malota-wojcik/isolator"
	"github.com/wojciech-malota-wojcik/isolator/executor/wire"
)

type cloneFromFn func(srcBuildKey types.BuildKey) (types.ImageManifest, error)

// NewBuilder creates new image builder
func NewBuilder(config config.Build, initializer base.Initializer, repo *Repository, storage storage.Driver, parser parser.Parser) *Builder {
	return &Builder{
		rebuild:     config.Rebuild,
		readyBuilds: map[types.BuildKey]bool{},
		initializer: initializer,
		repo:        repo,
		storage:     storage,
		parser:      parser,
	}
}

// Builder builds images
type Builder struct {
	rebuild     bool
	readyBuilds map[types.BuildKey]bool

	initializer base.Initializer
	repo        *Repository
	storage     storage.Driver
	parser      parser.Parser
}

// BuildFromFile builds image from spec file
func (b *Builder) BuildFromFile(ctx context.Context, specFile, name string, tags ...types.Tag) error {
	return b.buildFromFile(ctx, map[types.BuildKey]bool{}, specFile, name, tags...)
}

// Build builds images
func (b *Builder) Build(ctx context.Context, img *description.Descriptor) error {
	return b.build(ctx, map[types.BuildKey]bool{}, img)
}

func (b *Builder) buildFromFile(ctx context.Context, stack map[types.BuildKey]bool, specFile, name string, tags ...types.Tag) error {
	commands, err := b.parser.Parse(specFile)
	if err != nil {
		return err
	}
	return b.build(ctx, stack, description.Describe(name, tags, commands...))
}

func (b *Builder) initialize(ctx context.Context, buildKey types.BuildKey, path string) (retErr error) {
	if buildKey.Name == "scratch" {
		return nil
	}
	exit, err := chroot.Enter(path)
	if err != nil {
		return err
	}
	defer func() {
		if err := exit(); retErr == nil {
			retErr = err
		}
	}()
	return b.initializer.Init(ctx, buildKey)
}

func (b *Builder) build(ctx context.Context, stack map[types.BuildKey]bool, img *description.Descriptor) (retErr error) {
	if !types.IsNameValid(img.Name()) {
		return fmt.Errorf("name %s is invalid", img.Name())
	}
	tags := img.Tags()
	if len(tags) == 0 {
		tags = types.Tags{description.DefaultTag}
	}
	keys := make([]types.BuildKey, 0, len(tags))
	for _, tag := range tags {
		if !tag.IsValid() {
			return fmt.Errorf("tag %s is invalid", tag)
		}
		key := types.NewBuildKey(img.Name(), tag)
		if stack[key] {
			return fmt.Errorf("loop in dependencies detected on image %s", key)
		}
		stack[key] = true
		keys = append(keys, key)
	}

	buildID := types.NewBuildID()

	path, err := ioutil.TempDir("/tmp", "imagebuilder-*")
	if err != nil {
		return err
	}

	specDir := filepath.Join(path, ".specdir")

	var imgUnmount storage.UnmountFn
	defer func() {
		if err := umount(path); err != nil {
			if retErr == nil {
				retErr = err
			}
			return
		}
		if err := os.Remove(specDir); err != nil && !os.IsNotExist(err) {
			if retErr == nil {
				retErr = err
			}
			return
		}
		if imgUnmount != nil {
			if err := imgUnmount(); err != nil {
				if retErr == nil {
					retErr = err
				}
				return
			}
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			if retErr == nil {
				retErr = err
			}
			return
		}
		if retErr != nil {
			if err := b.storage.Drop(buildID); err != nil && !errors.Is(err, types.ErrImageDoesNotExist) {
				retErr = err
			}
			return
		}
	}()

	if len(img.Commands()) == 0 {
		if len(tags) != 1 {
			return errors.New("for base image exactly one tag is required")
		}
		if err := b.storage.CreateEmpty(img.Name(), buildID); err != nil {
			return err
		}
		var err error
		imgUnmount, err = b.storage.Mount(buildID, path)
		if err != nil {
			return err
		}

		if err := b.initialize(ctx, types.NewBuildKey(img.Name(), tags[0]), path); err != nil {
			return err
		}
	} else {
		ctxBuild, cancel := context.WithCancel(ctx)
		defer cancel()
		group := parallel.NewGroup(ctxBuild)
		var build *imageBuild
		build = newImageBuild(path, func(srcBuildKey types.BuildKey) (types.ImageManifest, error) {
			if !types.IsNameValid(srcBuildKey.Name) {
				return types.ImageManifest{}, fmt.Errorf("name %s is invalid", srcBuildKey.Name)
			}
			if !srcBuildKey.Tag.IsValid() {
				return types.ImageManifest{}, fmt.Errorf("tag %s is invalid", srcBuildKey.Tag)
			}

			// Try to clone existing image
			err := types.ErrImageDoesNotExist
			var srcBuildID types.BuildID
			if !b.rebuild || b.readyBuilds[srcBuildKey] {
				srcBuildID, err = b.storage.BuildID(srcBuildKey)
			}

			switch {
			case err == nil:
			case errors.Is(err, types.ErrImageDoesNotExist):
				// If image does not exist try to build it from file in the current directory but only if tag is a default one
				if srcBuildKey.Tag == description.DefaultTag {
					err = b.buildFromFile(ctx, stack, srcBuildKey.Name, srcBuildKey.Name, description.DefaultTag)
				}
			default:
				return types.ImageManifest{}, err
			}

			switch {
			case err == nil:
			case errors.Is(err, types.ErrImageDoesNotExist):
				// If spec file does not exist, try building from repository
				if baseImage := b.repo.Retrieve(srcBuildKey); baseImage != nil {
					err = b.build(ctx, stack, baseImage)
				} else {
					err = b.build(ctx, stack, description.Describe(srcBuildKey.Name, types.Tags{srcBuildKey.Tag}))
				}
			default:
				return types.ImageManifest{}, err
			}

			if err != nil {
				return types.ImageManifest{}, err
			}

			if !srcBuildID.IsValid() {
				srcBuildID, err = b.storage.BuildID(srcBuildKey)
				if err != nil {
					return types.ImageManifest{}, err
				}
			}

			if err := b.storage.Clone(srcBuildID, img.Name(), buildID); err != nil {
				return types.ImageManifest{}, err
			}

			imgUnmount, err = b.storage.Mount(buildID, path)
			if err != nil {
				return types.ImageManifest{}, err
			}

			manifest, err := b.storage.Manifest(srcBuildID)
			if err != nil {
				return types.ImageManifest{}, err
			}

			// To mount specdir readonly, trick is required:
			// 1. mount dir normally
			// 2. remount it using read-only option
			if err := os.Mkdir(specDir, 0o700); err != nil {
				return types.ImageManifest{}, err
			}
			if err := syscall.Mount(".", specDir, "", syscall.MS_BIND, ""); err != nil {
				return types.ImageManifest{}, err
			}
			if err := syscall.Mount(".", specDir, "", syscall.MS_BIND|syscall.MS_REMOUNT|syscall.MS_RDONLY, ""); err != nil {
				return types.ImageManifest{}, err
			}

			group.Spawn("isolator", parallel.Fail, func(ctx context.Context) error {
				return isolator.Run(ctx, isolator.Config{
					ExecutorPath: filepath.Join(path, "tmp", "executor"),
					Address:      "unix:///tmp/executor.sock",
					RootDir:      path,
				})
			})

			if err := retry.Do(ctx, 10, time.Second, func() error {
				var err error
				build.isolator, err = net.Dial("unix", filepath.Join(path, "tmp", "executor.sock"))
				return retry.Retryable(err)
			}); err != nil {
				return types.ImageManifest{}, err
			}

			return manifest, nil
		})

		group.Spawn("commands", parallel.Exit, func(ctx context.Context) error {
			for _, cmd := range img.Commands() {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}

				if err := cmd.Execute(build); err != nil {
					return err
				}
			}
			return nil
		})

		if err := group.Wait(); err != nil && (!errors.Is(err, ctxBuild.Err()) || errors.Is(err, ctx.Err())) {
			return err
		}

		build.manifest.BuildID = buildID
		if err := b.storage.StoreManifest(build.manifest); err != nil {
			return err
		}
	}

	for _, key := range keys {
		if err := b.storage.Tag(buildID, key.Tag); err != nil {
			return err
		}
	}
	for _, key := range keys {
		b.readyBuilds[key] = true
	}
	return nil
}

func umount(imgPath string) error {
	mountsRaw, err := ioutil.ReadFile("/proc/mounts")
	if err != nil {
		return err
	}
	for _, mount := range strings.Split(string(mountsRaw), "\n") {
		props := strings.SplitN(mount, " ", 3)
		if len(props) < 2 {
			// last empty line
			break
		}
		mountpoint := props[1]
		prefix := imgPath + "/"
		if !strings.HasPrefix(mountpoint, prefix) && mount != prefix {
			continue
		}
		if err := syscall.Unmount(mountpoint, 0); err != nil {
			return err
		}
	}
	return nil
}

func newImageBuild(path string, cloneFn cloneFromFn) *imageBuild {
	return &imageBuild{
		cloneFn: cloneFn,
		path:    path,
	}
}

type imageBuild struct {
	cloneFn cloneFromFn

	path     string
	manifest types.ImageManifest
	isolator net.Conn
}

// from is a handler for FROM
func (b *imageBuild) From(cmd *description.FromCommand) error {
	manifest, err := b.cloneFn(cmd.BuildKey)
	if err != nil {
		return err
	}
	b.manifest.BasedOn = manifest.BuildID
	b.manifest.Params = manifest.Params
	return nil
}

// params sets kernel params for image
func (b *imageBuild) Params(cmd *description.ParamsCommand) error {
	b.manifest.Params = append(b.manifest.Params, cmd.Params...)
	return nil
}

// run is a handler for RUN
func (b *imageBuild) Run(cmd *description.RunCommand) (retErr error) {
	decoder := json.NewDecoder(b.isolator)
	if _, err := b.isolator.Write(must.Bytes(json.Marshal(wire.RunMessage{Command: cmd.Command}))); err != nil {
		return err
	}
	var ack wire.Ack
	if err := decoder.Decode(&ack); err != nil {
		return err
	}
	if ack.Error != "" {
		return errors.New(ack.Error)
	}
	return nil
}
