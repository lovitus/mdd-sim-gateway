package com.lovitus.mddagent;
import java.util.Random;
/** Bounded full jitter; no periodic restart and no retry of business actions. */
public final class Retry {
    private int attempts; private final Random random;
    public Retry(){this(new Random());} public Retry(Random r){random=r;}
    public long next(){long cap=Math.min(120000L,1000L<<Math.min(attempts++,7));return 500+Math.floorMod(random.nextLong(),cap);}
    public void healthy(){attempts=0;}
}
