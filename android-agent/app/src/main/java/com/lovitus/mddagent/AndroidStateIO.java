package com.lovitus.mddagent;

import android.system.ErrnoException;
import android.system.Os;
import android.system.OsConstants;
import java.io.*;

/** Android's public checked syscalls, rather than AtomicFile's logging-only finishWrite. */
final class AndroidStateIO implements DurableFile.IO {
    public boolean exists(File file) throws IOException {
        try { Os.stat(file.getPath()); return true; }
        catch (ErrnoException e) { if (e.errno == OsConstants.ENOENT) return false; throw new IOException(e); }
    }
    public byte[] read(File file) throws IOException {
        try (FileInputStream in = new FileInputStream(file); ByteArrayOutputStream out = new ByteArrayOutputStream()) {
            byte[] chunk = new byte[8192]; int count;
            while ((count = in.read(chunk)) != -1) {
                if (out.size() + count > 1024 * 1024) throw new IOException("Private state exceeds size limit");
                out.write(chunk, 0, count);
            }
            return out.toByteArray();
        }
    }
    public void writeSynced(File file, byte[] bytes) throws IOException {
        try (FileOutputStream out = new FileOutputStream(file)) {
            out.write(bytes); out.getFD().sync();
        }
    }
    public void replace(File from, File to) throws IOException {
        try { Os.rename(from.getPath(), to.getPath()); }
        catch (ErrnoException e) { throw new IOException("Private-state publication failed", e); }
    }
    public void syncDirectory(File directory) throws IOException {
        try {
            FileDescriptor fd = Os.open(directory.getPath(), OsConstants.O_RDONLY, 0);
            try {
                if (!OsConstants.S_ISDIR(Os.fstat(fd).st_mode)) throw new IOException("Private-state parent is not a directory");
                Os.fsync(fd);
            } finally { Os.close(fd); }
        } catch (ErrnoException e) { throw new IOException("Private-state directory sync failed", e); }
    }
}
