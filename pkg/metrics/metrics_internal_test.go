package metrics

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartProfilingResourceDirError(t *testing.T) {
	stop, err := startProfiling(t.Context(), defaultTestProfilingDeps(t, profilingDeps{
		wakaResourcesDir: func(context.Context) (string, error) {
			return "", errors.New("home unavailable")
		},
	}))

	require.Error(t, err)
	assert.Nil(t, stop)
	assert.Contains(t, err.Error(), "failed getting user's home directory")
}

func TestStartProfilingCPUFileError(t *testing.T) {
	stop, err := startProfiling(t.Context(), defaultTestProfilingDeps(t, profilingDeps{
		mkdirAll: func(string, os.FileMode) error { return nil },
		createFile: func(string) (io.WriteCloser, error) {
			return nil, errors.New("create cpu")
		},
	}))

	require.Error(t, err)
	assert.Nil(t, stop)
	assert.Contains(t, err.Error(), "failed to create cpu profile file")
}

func TestStartProfilingMemFileError(t *testing.T) {
	for _, cpuBusy := range []bool{false, true} {
		name := "owned CPU profiler"
		if cpuBusy {
			name = "external CPU profiler"
		}

		t.Run(name, func(t *testing.T) {
			cpuFile, err := os.CreateTemp(t.TempDir(), "cpu")
			require.NoError(t, err)
			t.Cleanup(func() { _ = cpuFile.Close() })
			var calls, stops int

			stop, err := startProfiling(t.Context(), defaultTestProfilingDeps(t, profilingDeps{
				mkdirAll: func(string, os.FileMode) error { return nil },
				startCPUProfile: func(io.Writer) error {
					if cpuBusy {
						return errors.New("cpu busy")
					}

					return nil
				},
				stopCPUProfile: func() { stops++ },
				createFile: func(string) (io.WriteCloser, error) {
					calls++
					if calls == 2 {
						return nil, errors.New("create mem")
					}

					return cpuFile, nil
				},
			}))
			require.ErrorContains(t, err, "failed to create mem profile file")
			assert.Nil(t, stop)

			_, writeErr := cpuFile.Write([]byte("closed"))
			assert.ErrorIs(t, writeErr, os.ErrClosed)
			if cpuBusy {
				assert.Zero(t, stops)
			} else {
				assert.Equal(t, 1, stops)
			}
		})
	}
}

func TestStartProfilingProfileAndCloseErrors(t *testing.T) {
	var stopped bool

	stop, err := startProfiling(t.Context(), defaultTestProfilingDeps(t, profilingDeps{
		createFile:       func(string) (io.WriteCloser, error) { return errWriteCloser{}, nil },
		startCPUProfile:  func(io.Writer) error { return errors.New("cpu busy") },
		writeHeapProfile: func(io.Writer) error { return errors.New("heap failed") },
		stopCPUProfile:   func() { stopped = true },
	}))

	require.NoError(t, err)
	require.NotNil(t, stop)

	stop()
	assert.False(t, stopped)
}

func TestStartProfilingCreatesMetricsFolderThroughHook(t *testing.T) {
	home := t.TempDir()

	stop, err := startProfiling(t.Context(), defaultTestProfilingDeps(t, profilingDeps{
		wakaResourcesDir: func(context.Context) (string, error) { return home, nil },
	}))
	require.NoError(t, err)
	require.NotNil(t, stop)
	stop()

	assert.DirExists(t, filepath.Join(home, "metrics"))
}

type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                { return nil }

type errWriteCloser struct{}

func (errWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (errWriteCloser) Close() error                { return errors.New("close failed") }

func defaultTestProfilingDeps(t *testing.T, override profilingDeps) profilingDeps {
	t.Helper()

	deps := profilingDeps{
		wakaResourcesDir: func(context.Context) (string, error) { return t.TempDir(), nil },
		mkdirAll:         os.MkdirAll,
		createFile: func(string) (io.WriteCloser, error) {
			return nopWriteCloser{}, nil
		},
		startCPUProfile:  func(io.Writer) error { return nil },
		writeHeapProfile: func(io.Writer) error { return nil },
		stopCPUProfile:   func() {},
	}

	if override.wakaResourcesDir != nil {
		deps.wakaResourcesDir = override.wakaResourcesDir
	}

	if override.mkdirAll != nil {
		deps.mkdirAll = override.mkdirAll
	}

	if override.createFile != nil {
		deps.createFile = override.createFile
	}

	if override.startCPUProfile != nil {
		deps.startCPUProfile = override.startCPUProfile
	}

	if override.writeHeapProfile != nil {
		deps.writeHeapProfile = override.writeHeapProfile
	}

	if override.stopCPUProfile != nil {
		deps.stopCPUProfile = override.stopCPUProfile
	}

	return deps
}
