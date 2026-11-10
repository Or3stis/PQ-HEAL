#!/usr/bin/env python3
"""Sample interval CPU (100% = one core), RSS and threads from Linux /proc."""
import csv,datetime,os,pathlib,sys,time
pid=int(sys.argv[1]);seconds=int(sys.argv[2])
if not 1<=seconds<=86400: raise SystemExit('Duration must be 1..86400 seconds')
ticks=os.sysconf('SC_CLK_TCK');page=os.sysconf('SC_PAGE_SIZE')
def read():
    fields=pathlib.Path(f'/proc/{pid}/stat').read_text().rsplit(')',1)[1].split()
    return (int(fields[11])+int(fields[12]))/ticks,int(fields[21])*page,int(fields[17]),fields[19]
w=csv.writer(sys.stdout);w.writerow(['utc','elapsed_s','cpu_percent_one_core','rss_bytes','threads'])
prev,rss,threads,identity=read();start=last=time.monotonic()
for _ in range(seconds):
    time.sleep(1);cur,rss,threads,current=read();now=time.monotonic()
    if current!=identity: raise SystemExit('PID reused; measurement invalid')
    w.writerow([datetime.datetime.now(datetime.timezone.utc).isoformat(),round(now-start,3),round(100*(cur-prev)/(now-last),3),rss,threads]);sys.stdout.flush()
    prev,last=cur,now
