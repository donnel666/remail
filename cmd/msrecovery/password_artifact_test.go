package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const artifactTestPassword = "Aa1!Bb2@Cc3#Dd4$Ee5%"

func TestCreatePasswordArtifactUsesExclusive0600ExactFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new-password")

	artifact, err := createPasswordArtifact(path, artifactTestPassword)
	require.NoError(t, err)
	require.NotNil(t, artifact)
	t.Cleanup(func() { _ = artifact.Remove() })

	info, err := os.Lstat(path)
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular())
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, []byte(artifactTestPassword), raw)

	_, err = createPasswordArtifact(path, "Zz9=Yy8-Xx7_Ww6+Vv5*")
	require.Error(t, err)
	assert.ErrorIs(t, err, os.ErrExist)
	assert.NotContains(t, err.Error(), artifactTestPassword)

	rawAfterConflict, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, []byte(artifactTestPassword), rawAfterConflict)
}

func TestCreatePasswordArtifactRejectsAmbiguousContent(t *testing.T) {
	for name, password := range map[string]string{
		"empty":   "",
		"newline": artifactTestPassword + "\n",
		"crlf":    artifactTestPassword + "\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "new-password")
			_, err := createPasswordArtifact(path, password)
			require.Error(t, err)
			assert.NoFileExists(t, path)
			if password != "" {
				assert.NotContains(t, err.Error(), password)
			}
		})
	}
}

func TestLoadPasswordArtifactReturnsExactContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new-password")
	require.NoError(t, os.WriteFile(path, []byte(artifactTestPassword), 0o600))

	artifact, password, err := loadPasswordArtifact(path)
	require.NoError(t, err)
	require.NotNil(t, artifact)
	assert.Equal(t, artifactTestPassword, password)

	require.NoError(t, artifact.Remove())
	assert.NoFileExists(t, path)
	// Cleanup is intentionally idempotent for a post-commit retry.
	require.NoError(t, artifact.Remove())
}

func TestLoadPasswordArtifactAllowsNarrowerOwnerPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new-password")
	require.NoError(t, os.WriteFile(path, []byte(artifactTestPassword), 0o600))
	require.NoError(t, os.Chmod(path, 0o400))

	artifact, password, err := loadPasswordArtifact(path)
	require.NoError(t, err)
	assert.Equal(t, artifactTestPassword, password)
	t.Cleanup(func() { _ = artifact.Remove() })
}

func TestLoadPasswordArtifactRejectsBroadPermissions(t *testing.T) {
	for _, mode := range []os.FileMode{0o700, 0o640, 0o604, 0o601} {
		t.Run(mode.String(), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "new-password")
			require.NoError(t, os.WriteFile(path, []byte(artifactTestPassword), 0o600))
			require.NoError(t, os.Chmod(path, mode))

			artifact, password, err := loadPasswordArtifact(path)
			assert.Nil(t, artifact)
			assert.Empty(t, password)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), artifactTestPassword)
		})
	}
}

func TestLoadPasswordArtifactRejectsNonRegularFiles(t *testing.T) {
	directoryPath := filepath.Join(t.TempDir(), "artifact-directory")
	require.NoError(t, os.Mkdir(directoryPath, 0o700))

	artifact, password, err := loadPasswordArtifact(directoryPath)
	assert.Nil(t, artifact)
	assert.Empty(t, password)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), artifactTestPassword)

	target := filepath.Join(t.TempDir(), "target")
	require.NoError(t, os.WriteFile(target, []byte(artifactTestPassword), 0o600))
	symlink := filepath.Join(t.TempDir(), "artifact-symlink")
	require.NoError(t, os.Symlink(target, symlink))

	artifact, password, err = loadPasswordArtifact(symlink)
	assert.Nil(t, artifact)
	assert.Empty(t, password)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), artifactTestPassword)
}

func TestLoadPasswordArtifactRejectsLineTerminatorInsteadOfTrimming(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new-password")
	require.NoError(t, os.WriteFile(path, []byte(artifactTestPassword+"\n"), 0o600))

	artifact, password, err := loadPasswordArtifact(path)
	assert.Nil(t, artifact)
	assert.Empty(t, password)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), artifactTestPassword)
}

func TestPasswordArtifactRemoveRefusesReplacementFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new-password")
	originalPath := filepath.Join(dir, "original-password")
	replacementPassword := "Zz9=Yy8-Xx7_Ww6+Vv5*"

	artifact, err := createPasswordArtifact(path, artifactTestPassword)
	require.NoError(t, err)
	require.NoError(t, os.Rename(path, originalPath))
	require.NoError(t, os.WriteFile(path, []byte(replacementPassword), 0o600))

	err = artifact.Remove()
	require.Error(t, err)
	assert.NotContains(t, err.Error(), artifactTestPassword)
	assert.NotContains(t, err.Error(), replacementPassword)

	replacement, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, []byte(replacementPassword), replacement)
	assert.FileExists(t, originalPath)
}

func TestPasswordArtifactRemoveTreatsMissingFileAsClean(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new-password")
	artifact, err := createPasswordArtifact(path, artifactTestPassword)
	require.NoError(t, err)
	require.NoError(t, os.Remove(path))

	assert.NoError(t, artifact.Remove())
}

func TestPasswordArtifactErrorsNeverContainPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new-password")
	require.NoError(t, os.WriteFile(path, []byte(artifactTestPassword), 0o666))

	_, _, err := loadPasswordArtifact(path)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), artifactTestPassword)

	invalid := &passwordArtifact{}
	err = invalid.Remove()
	require.Error(t, err)
	assert.NotContains(t, err.Error(), artifactTestPassword)
	assert.False(t, errors.Is(err, os.ErrNotExist))
}
