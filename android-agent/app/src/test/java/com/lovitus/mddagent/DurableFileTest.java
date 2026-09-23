package com.lovitus.mddagent;

import org.junit.Test;
import java.io.*;
import java.util.*;
import static org.junit.Assert.*;

public class DurableFileTest {
    private static final class Disk implements DurableFile.IO {
        final Map<String,byte[]> data = new HashMap<>();
        int step, failAt = -1;
        boolean published;
        private void boundary() throws IOException { if (++step == failAt) throw new IOException("injected disk boundary " + step); }
        public boolean exists(File f) { return data.containsKey(f.getPath()); }
        public byte[] read(File f) throws IOException { if (!exists(f)) throw new FileNotFoundException(); return data.get(f.getPath()).clone(); }
        public void writeSynced(File f,byte[] b)throws IOException { data.put(f.getPath(),b.clone()); boundary(); }
        public void replace(File from,File to)throws IOException { boundary(); data.put(to.getPath(),data.remove(from.getPath())); published=true; }
        public void syncDirectory(File directory)throws IOException { boundary(); }
    }
    @Test public void noDiskBoundaryFailureCanReportSuccess()throws Exception {
        for(int fail=1;fail<=6;fail++){
            Disk disk=new Disk();disk.failAt=fail;DurableFile file=new DurableFile(new File("/private/state"),disk);
            try{file.write(new byte[]{42});fail("failure at "+fail+" admitted a dependent operation");}catch(IOException expected){}
            if(disk.published)assertArrayEquals(new byte[]{42},file.read());
        }
    }
    @Test public void publicationAndDirectorySyncPrecedeSuccessfulReadback()throws Exception {
        Disk disk=new Disk();DurableFile file=new DurableFile(new File("/private/state"),disk);
        file.write(new byte[]{1});assertTrue(disk.published);assertEquals(6,disk.step);
        file.write(new byte[]{2});assertArrayEquals(new byte[]{2},file.read());
    }
    @Test public void interruptedFirstCommitHasRecoveryMaterialNotFreshSetup()throws Exception {
        Disk disk=new Disk();disk.failAt=5;DurableFile file=new DurableFile(new File("/private/state"),disk);
        try{file.write(new byte[]{7});fail();}catch(IOException expected){}
        assertFalse(file.exists());assertTrue(file.hasRecoveryMaterial());
        disk.failAt=-1;file.publish(file.stagedBytes());assertArrayEquals(new byte[]{7},file.read());
    }
}
