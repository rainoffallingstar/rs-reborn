package lockfile

import internallockfile "github.com/rainoffallingstar/rs-reborn/internal/lockfile"

type File = internallockfile.File
type Metadata = internallockfile.Metadata
type Package = internallockfile.Package

func Write(path string, file File) error {
	return internallockfile.Write(path, file)
}

func Read(path string) (File, error) {
	return internallockfile.Read(path)
}
