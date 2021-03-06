package repobuilder

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/pkg/errors"
	"github.com/tychoish/grip"
)

// BuildDEBRepoJob is an amboy.Job implementation that builds Debian
// and Ubuntu repositories.
type BuildDEBRepoJob struct {
	*Job
}

func setupDEBJob(j *Job) {
	r := &BuildDEBRepoJob{j}
	r.Job.builder = r
}

func (j *BuildDEBRepoJob) createArchDirs(basePath string) error {
	catcher := grip.NewCatcher()

	for _, arch := range j.Distro.Architectures {
		path := filepath.Join(basePath, "binary-"+arch)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			err = os.MkdirAll(path, 0755)
			if err != nil {
				catcher.Add(err)
				continue
			}

			// touch the Packages file
			err = ioutil.WriteFile(filepath.Join(path, "Packages"), []byte(""), 0644)
			if err != nil {
				catcher.Add(err)
				continue
			}

			catcher.Add(gzipAndWriteToFile(filepath.Join(path, "Packages.gz"), []byte("")))
		}
	}

	return catcher.Resolve()
}

func (j *BuildDEBRepoJob) injectPackage(local, repoName string) (string, error) {
	catcher := grip.NewCatcher()

	repoPath := filepath.Join(local, repoName, j.Distro.Component)
	err := j.createArchDirs(repoPath)
	catcher.Add(j.linkPackages(filepath.Join(repoPath, "binary-"+j.Arch)))
	catcher.Add(err)

	return repoPath, catcher.Resolve()
}

func gzipAndWriteToFile(fileName string, content []byte) error {
	var gz bytes.Buffer

	w, err := gzip.NewWriterLevel(&gz, flate.BestCompression)
	if err != nil {
		return errors.Wrapf(err, "compressing file '%s'", fileName)
	}

	_, err = w.Write(content)
	if err != nil {
		return errors.Wrapf(err, "writing content '%s", fileName)
	}
	err = w.Close()
	if err != nil {
		return errors.Wrapf(err, "closing buffer '%s", fileName)
	}

	err = ioutil.WriteFile(fileName, gz.Bytes(), 0644)
	if err != nil {
		return errors.Wrapf(err, "writing compressed file '%s'", fileName)
	}

	grip.Noticeln("wrote zipped packages file to:", fileName)
	return nil
}

func (j *BuildDEBRepoJob) rebuildRepo(workingDir string) error {
	arch := "binary-" + j.Arch

	// start by running dpkg-scanpackages to generate a packages file
	// in the source.
	dirParts := strings.Split(workingDir, string(filepath.Separator))
	cmd := exec.Command("dpkg-scanpackages", "--multiversion", filepath.Join(filepath.Join(dirParts[len(dirParts)-5:]...), arch))
	cmd.Dir = string(filepath.Separator) + filepath.Join(dirParts[:len(dirParts)-5]...)

	grip.Infof("running command='%s' path='%s'", strings.Join(cmd.Args, " "), cmd.Dir)
	out, err := cmd.Output()
	if err != nil {
		return errors.Wrapf(err, "building 'Packages': [%s]", string(out))
	}

	// Write the packages file to disk.
	pkgsFile := filepath.Join(workingDir, arch, "Packages")
	if err = ioutil.WriteFile(pkgsFile, out, 0644); err != nil {
		return errors.Wrapf(err, "problem writing packages file to '%s'", pkgsFile)
	}
	grip.Noticeln("wrote packages file to:", pkgsFile)

	// Compress/gzip the packages file

	if err = gzipAndWriteToFile(pkgsFile+".gz", out); err != nil {
		return errors.Wrap(err, "compressing the 'Packages' file")
	}

	// Continue by building the Releases file, first by using the
	// template, and then by
	releaseTmplSrc, ok := j.Conf.Templates.Deb[j.Distro.Edition]
	if !ok {
		return errors.Errorf("no 'Release' template defined for %s", j.Distro.Edition)
	}

	// initialize the template.
	tmpl, err := template.New("Releases").Parse(releaseTmplSrc)
	if err != nil {
		return errors.Wrap(err, "reading Releases template")
	}

	buffer := bytes.NewBuffer([]byte{})
	err = tmpl.Execute(buffer, struct {
		CodeName      string
		Component     string
		Architectures string
	}{
		CodeName:      j.Distro.CodeName,
		Component:     j.Distro.Component,
		Architectures: strings.Join(j.Distro.Architectures, " "),
	})
	if err != nil {
		return errors.Wrap(err, "rendering Releases template")
	}

	// This builds a Release file using the header info generated
	// from the template above.
	cmd = exec.Command("apt-ftparchive", "release", "../")
	cmd.Dir = workingDir
	out, err = cmd.Output()
	grip.Infof("generating release file: [command='%s', path='%s']", strings.Join(cmd.Args, " "), cmd.Dir)
	outString := string(out)
	grip.Debug(outString)
	if err != nil {
		return errors.Wrapf(err, "generating Release content for %s", workingDir)
	}

	// get the content from the template and add the output of
	// apt-ftparchive there.
	releaseContent := buffer.Bytes()
	releaseContent = append(releaseContent, out...)

	// tracking the output is useful. we'll do that here.
	j.mutex.Lock()
	j.Output["sign-release-file-"+workingDir] = outString
	j.mutex.Unlock()

	// write the content of the release file to disk.
	relFileName := filepath.Join(filepath.Dir(workingDir), "Release")

	if err = ioutil.WriteFile(relFileName, releaseContent, 0644); err != nil {
		return errors.Wrapf(err, "writing Release file to disk %s", relFileName)
	}

	grip.Noticeln("wrote release files to:", relFileName)

	// sign the file using the notary service. To remove the
	// MongoDB-specificity we could make this configurable, or
	// offer ways of specifying different signing option.
	//
	// signFile(name, extension, overwrite)
	if err = j.signFile(relFileName, "gpg", false); err != nil {
		return errors.Wrapf(err, "signing Release file for %s", workingDir)
	}

	// build the index page.
	if err = j.Conf.BuildIndexPageForDirectory(workingDir, j.Distro.Bucket); err != nil {
		return errors.Wrapf(err, "building index.html pages for %s", workingDir)
	}

	return nil
}
