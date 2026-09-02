package sshclient

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// SFTP 封装 github.com/pkg/sftp.Client 的常用操作。
type SFTP struct {
	client *sftp.Client
}

// NewSFTP 在既有 SSH 连接上建立 SFTP 会话。
func NewSFTP(sshClient *ssh.Client) (*SFTP, error) {
	c, err := sftp.NewClient(sshClient)
	if err != nil {
		return nil, fmt.Errorf("open sftp session: %w", err)
	}
	return &SFTP{client: c}, nil
}

// Client 返回底层 sftp.Client。
func (s *SFTP) Client() *sftp.Client { return s.client }

// Close 关闭 SFTP 会话。
func (s *SFTP) Close() error { return s.client.Close() }

// List 列出远程目录内容，返回 FileInfo 列表（目录名为 "." 时用 "." 表示）。
func (s *SFTP) List(remotePath string) ([]os.FileInfo, error) {
	infos, err := s.client.ReadDir(remotePath)
	if err != nil {
		return nil, fmt.Errorf("read dir %q: %w", remotePath, err)
	}
	return infos, nil
}

// Stat 返回远程路径信息。
func (s *SFTP) Stat(remotePath string) (os.FileInfo, error) {
	fi, err := s.client.Stat(remotePath)
	if err != nil {
		return nil, fmt.Errorf("stat %q: %w", remotePath, err)
	}
	return fi, nil
}

// DownloadFile 下载单个远程文件到本地路径。
func (s *SFTP) DownloadFile(remotePath, localPath string, force bool) error {
	if !force {
		if _, err := os.Stat(localPath); err == nil {
			return fmt.Errorf("local file %q already exists (use force to overwrite)", localPath)
		}
	}
	r, err := s.client.Open(remotePath)
	if err != nil {
		return fmt.Errorf("open remote %q: %w", remotePath, err)
	}
	defer r.Close()

	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return fmt.Errorf("mkdir local %q: %w", filepath.Dir(localPath), err)
	}
	w, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("create local %q: %w", localPath, err)
	}
	defer w.Close()

	if _, err := io.Copy(w, r); err != nil {
		return fmt.Errorf("download %q -> %q: %w", remotePath, localPath, err)
	}
	return nil
}

// UploadFile 上传单个本地文件到远程路径。
func (s *SFTP) UploadFile(localPath, remotePath string, force bool) error {
	if !force {
		if _, err := s.client.Stat(remotePath); err == nil {
			return fmt.Errorf("remote file %q already exists (use force to overwrite)", remotePath)
		}
	}
	r, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open local %q: %w", localPath, err)
	}
	defer r.Close()

	if err := s.client.MkdirAll(path.Dir(remotePath)); err != nil {
		return fmt.Errorf("mkdir remote %q: %w", path.Dir(remotePath), err)
	}
	w, err := s.client.Create(remotePath)
	if err != nil {
		return fmt.Errorf("create remote %q: %w", remotePath, err)
	}
	defer w.Close()

	if _, err := io.Copy(w, r); err != nil {
		return fmt.Errorf("upload %q -> %q: %w", localPath, remotePath, err)
	}
	return nil
}

// DownloadDir 递归下载远程目录到本地。
func (s *SFTP) DownloadDir(remoteDir, localDir string, force bool) error {
	infos, err := s.client.ReadDir(remoteDir)
	if err != nil {
		return fmt.Errorf("read remote dir %q: %w", remoteDir, err)
	}
	if err := os.MkdirAll(localDir, 0755); err != nil {
		return err
	}
	for _, fi := range infos {
		remotePath := path.Join(remoteDir, fi.Name())
		localPath := filepath.Join(localDir, fi.Name())
		if fi.IsDir() {
			if err := s.DownloadDir(remotePath, localPath, force); err != nil {
				return err
			}
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			if target, err := s.client.ReadLink(remotePath); err == nil {
				if err := os.Symlink(target, localPath); err != nil {
					return err
				}
			}
			continue
		}
		if err := s.DownloadFile(remotePath, localPath, force); err != nil {
			return err
		}
	}
	return nil
}

// UploadDir 递归上传本地目录到远程。
func (s *SFTP) UploadDir(localDir, remoteDir string, force bool) error {
	entries, err := os.ReadDir(localDir)
	if err != nil {
		return fmt.Errorf("read local dir %q: %w", localDir, err)
	}
	if err := s.client.MkdirAll(remoteDir); err != nil {
		return err
	}
	for _, entry := range entries {
		localPath := filepath.Join(localDir, entry.Name())
		remotePath := path.Join(remoteDir, entry.Name())
		if entry.IsDir() {
			if err := s.UploadDir(localPath, remotePath, force); err != nil {
				return err
			}
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if target, err := os.Readlink(localPath); err == nil {
				if err := s.client.Symlink(target, remotePath); err != nil {
					return err
				}
			}
			continue
		}
		if err := s.UploadFile(localPath, remotePath, force); err != nil {
			return err
		}
	}
	return nil
}

// Remove 删除远程文件或空目录。
func (s *SFTP) Remove(remotePath string) error {
	if err := s.client.Remove(remotePath); err != nil {
		return fmt.Errorf("remove %q: %w", remotePath, err)
	}
	return nil
}

// RemoveDir 删除远程目录（要求为空）。
func (s *SFTP) RemoveDir(remotePath string) error {
	if err := s.client.RemoveDirectory(remotePath); err != nil {
		return fmt.Errorf("remove dir %q: %w", remotePath, err)
	}
	return nil
}

// Mkdir 创建远程目录。
func (s *SFTP) Mkdir(remotePath string) error {
	if err := s.client.Mkdir(remotePath); err != nil {
		return fmt.Errorf("mkdir %q: %w", remotePath, err)
	}
	return nil
}

// MkdirAll 递归创建远程目录。
func (s *SFTP) MkdirAll(remotePath string) error {
	if err := s.client.MkdirAll(remotePath); err != nil {
		return fmt.Errorf("mkdir -p %q: %w", remotePath, err)
	}
	return nil
}

// Rename 重命名远程文件/目录。
func (s *SFTP) Rename(oldPath, newPath string) error {
	if err := s.client.Rename(oldPath, newPath); err != nil {
		return fmt.Errorf("rename %q -> %q: %w", oldPath, newPath, err)
	}
	return nil
}

// Chmod 修改远程文件权限。
func (s *SFTP) Chmod(remotePath string, mode os.FileMode) error {
	if err := s.client.Chmod(remotePath, mode); err != nil {
		return fmt.Errorf("chmod %q: %w", remotePath, err)
	}
	return nil
}

// Chown 修改远程文件 owner/group。
func (s *SFTP) Chown(remotePath string, uid, gid int) error {
	if err := s.client.Chown(remotePath, uid, gid); err != nil {
		return fmt.Errorf("chown %q: %w", remotePath, err)
	}
	return nil
}

// Symlink 创建远程符号链接。
func (s *SFTP) Symlink(target, link string) error {
	if err := s.client.Symlink(target, link); err != nil {
		return fmt.Errorf("symlink %q -> %q: %w", target, link, err)
	}
	return nil
}

// ReadLink 读取远程符号链接目标。
func (s *SFTP) ReadLink(p string) (string, error) {
	target, err := s.client.ReadLink(p)
	if err != nil {
		return "", fmt.Errorf("readlink %q: %w", p, err)
	}
	return target, nil
}

// Chtimes 修改远程文件访问/修改时间。
func (s *SFTP) Chtimes(p string, atime, mtime time.Time) error {
	if err := s.client.Chtimes(p, atime, mtime); err != nil {
		return fmt.Errorf("chtimes %q: %w", p, err)
	}
	return nil
}

// SanitizeRemotePath 规范化远程路径（保持 path.Join 语义但不清理 "."）。
func SanitizeRemotePath(p string) string {
	if p == "" || p == "." {
		return "."
	}
	return path.Clean(strings.ReplaceAll(p, "\\", "/"))
}

// SanitizeLocalPath 规范化本地路径。
func SanitizeLocalPath(p string) string {
	if p == "" {
		return "."
	}
	return filepath.Clean(p)
}