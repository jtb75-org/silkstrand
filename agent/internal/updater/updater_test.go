package updater

import (
	"os"
	"testing"
)

func TestDetectContainer(t *testing.T) {
	// files maps a path to its contents; a key's presence means the path exists.
	type files map[string]string

	tests := []struct {
		name  string
		env   map[string]string
		files files
		want  bool
	}{
		{
			name:  "k8s pod env with bare cgroup v2",
			env:   map[string]string{"KUBERNETES_SERVICE_HOST": "10.96.0.1"},
			files: files{"/proc/1/cgroup": "0::/\n", "/proc/self/cgroup": "0::/\n"},
			want:  true,
		},
		{
			name:  "docker dockerenv marker",
			files: files{"/.dockerenv": "", "/proc/1/cgroup": "0::/\n"},
			want:  true,
		},
		{
			name:  "podman containerenv marker",
			files: files{"/run/.containerenv": "", "/proc/1/cgroup": "0::/\n"},
			want:  true,
		},
		{
			name:  "cgroup v1 docker path",
			files: files{"/proc/1/cgroup": "12:pids:/docker/abcdef0123\n"},
			want:  true,
		},
		{
			name:  "cgroup v2 kubepods path",
			files: files{"/proc/1/cgroup": "0::/kubepods/besteffort/pod1234/abcd\n"},
			want:  true,
		},
		{
			name:  "containerd marker only in self cgroup",
			files: files{"/proc/1/cgroup": "0::/\n", "/proc/self/cgroup": "0::/system.slice/containerd.service\n"},
			want:  true,
		},
		{
			name:  "bare metal nothing set",
			files: files{"/proc/1/cgroup": "0::/init.scope\n", "/proc/self/cgroup": "0::/user.slice\n"},
			want:  false,
		},
		{
			// Intentional: detection errs toward container (see detectContainer's
			// asymmetric-failure rationale). A bare-metal host whose cgroup path
			// happens to contain a marker substring — e.g. a systemd unit named
			// docker.service on a Docker-capable host — is reported as a container.
			// That's a harmless false positive (UI offers "Recreate from image");
			// the match stays broad rather than risk the harmful false negative.
			name:  "marker-like substring in bare-metal cgroup path is intentionally true",
			files: files{"/proc/1/cgroup": "0::/system.slice/docker.service\n"},
			want:  true,
		},
		{
			name: "bare metal with no cgroup files readable",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(k string) string { return tt.env[k] }
			exists := func(p string) bool { _, ok := tt.files[p]; return ok }
			read := func(p string) ([]byte, error) {
				v, ok := tt.files[p]
				if !ok {
					return nil, os.ErrNotExist
				}
				return []byte(v), nil
			}
			if got := detectContainer(getenv, exists, read); got != tt.want {
				t.Errorf("detectContainer() = %v, want %v", got, tt.want)
			}
		})
	}
}
