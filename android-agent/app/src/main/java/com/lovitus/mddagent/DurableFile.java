package com.lovitus.mddagent;

import java.io.File;
import java.io.IOException;
import java.util.Arrays;

/** Checked single-file publication. The caller serializes access and authenticates the payload. */
final class DurableFile {
    interface IO {
        boolean exists(File file) throws IOException;
        byte[] read(File file) throws IOException;
        void writeSynced(File file, byte[] bytes) throws IOException;
        void replace(File from, File to) throws IOException;
        void syncDirectory(File directory) throws IOException;
    }
    private final IO io;
    final File base;
    DurableFile(File base, IO io) { this.base = base; this.io = io; }
    File staged() { return new File(base + ".new"); }
    File owned() { return new File(base + ".owned"); }
    boolean exists() throws IOException { return io.exists(base); }
    boolean hasRecoveryMaterial() throws IOException {
        return io.exists(staged()) || io.exists(owned()) || io.exists(new File(base + ".bak"));
    }
    byte[] read() throws IOException { return io.read(base); }
    byte[] stagedBytes() throws IOException { return io.read(staged()); }

    void write(byte[] encrypted) throws IOException {
        if (!io.exists(owned())) {
            io.writeSynced(owned(), new byte[]{1});
            io.syncDirectory(base.getParentFile());
        }
        io.writeSynced(staged(), encrypted);
        io.syncDirectory(base.getParentFile());
        publish(encrypted);
    }

    // Also used only after the caller has authenticated a complete interrupted first write.
    void publish(byte[] expected) throws IOException {
        if (!io.exists(owned())) throw new IOException("Missing private-state ownership marker");
        io.replace(staged(), base);
        io.syncDirectory(base.getParentFile());
        if (!Arrays.equals(expected, io.read(base))) throw new IOException("Private-state commit verification failed");
    }
}
