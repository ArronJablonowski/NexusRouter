package maintenance

import (
	"context"
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/scripts"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

func TestScheduleRequiresQualifiedUtilities(t *testing.T) {
	if _, err := (Service{Home: t.TempDir()}).Schedule(context.Background(), nil); err == nil {
		t.Fatal("uninstalled schedule control accepted")
	}
}
func TestScheduleRequestRequiresExplicitValidFields(t *testing.T) {
	for _, raw := range []string{`{"version":1,"settings":{}}`, `{"version":1,"settings":{"enabled":null,"hour":3,"minute":30}}`, `{"version":1,"settings":{"enabled":true,"hour":24,"minute":30}}`} {
		var u ScheduleUpdate
		if json.Unmarshal([]byte(raw), &u) == nil {
			t.Fatal("accepted", raw)
		}
	}
}

func TestNativeCleanupScheduleLifecycle(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("LaunchAgent scheduling is macOS-only")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	label := "com.nexusrouter.cleanup-qa"
	domain := "gui/" + strconv.Itoa(os.Getuid())
	t.Cleanup(func() {
		_ = exec.Command("/bin/launchctl", "bootout", domain+"/"+label).Run()
		_ = exec.Command("/bin/launchctl", "enable", domain+"/"+label).Run()
	})
	helper := filepath.Join(base, "cleanup-schedule.py")
	if err := os.WriteFile(helper, scripts.CleanupScheduleSource, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "cleanup-nexus-home.py"), scripts.CleanupSource, 0700); err != nil {
		t.Fatal(err)
	}
	code := `import datetime,importlib.util,json,os,pathlib,plistlib,subprocess,sys
base=pathlib.Path(sys.argv[1]);spec=importlib.util.spec_from_file_location('schedule',base/'cleanup-schedule.py');m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m);m.LABEL='com.nexusrouter.cleanup-qa'
root=base/'.NexusRouter';state=root/'tools/maintenance';agents=base/'Library/LaunchAgents';state.mkdir(parents=True,mode=0o700);agents.mkdir(parents=True,mode=0o700)
marker=base/'must-not-run';(state/'cleanup-nexus-home.py').write_text('from pathlib import Path;Path('+repr(str(marker))+').touch()')
value={'Label':m.LABEL,'ProgramArguments':['/usr/bin/python3','-B',str(state/'cleanup-nexus-home.py'),'--home',str(root),'--apply'],'StartCalendarInterval':{'Hour':(datetime.datetime.now().hour+12)%24,'Minute':37},'ProcessType':'Background','Nice':10,'LowPriorityIO':True,'StandardOutPath':str(state/'scheduler.log'),'StandardErrorPath':str(state/'scheduler-error.log')}
agent=agents/(m.LABEL+'.plist');agent.write_bytes(plistlib.dumps(value));agent.chmod(0o600)
s=m.Scheduler(root,user_home=base)
subprocess.run(['/bin/launchctl','enable',s.job],check=True)
subprocess.run(['/bin/launchctl','bootstrap',s.domain,str(agent)],check=True)
try:
 first=s.status()['schedule'];assert first['loaded'] and first['enabled'] and first['state']=='ready',first
 for enabled,day in [(False,None),(False,2),(True,2),(True,None)]:
  before=s.status()['schedule'];settings={'enabled':enabled,'hour':value['StartCalendarInterval']['Hour'],'minute':37,'weekday':day};after=s.apply(settings,before['digest'])['schedule'];assert after['loaded']==enabled and after['enabled']==enabled and after['weekday']==day and after['state']=='ready',after
 assert not marker.exists(),'schedule edit executed a cleanup'
 print('Native owned LaunchAgent daily/weekly and on/off lifecycle passed; no cleanup execution.')
finally:
 subprocess.run(['/bin/launchctl','bootout',s.job],capture_output=True)
 subprocess.run(['/bin/launchctl','enable',s.job],capture_output=True)
`
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "/usr/bin/python3", "-B", "-c", code, base).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, output)
	}
}

func TestScheduleCancellationCleansOwnedSubprocessTree(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS process group implementation")
	}
	base := t.TempDir()
	ready := filepath.Join(base, "ready")
	late := filepath.Join(base, "late")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	code := `import pathlib,subprocess,sys,time
child="import pathlib,time;pathlib.Path("+repr(sys.argv[1])+").touch();time.sleep(1);pathlib.Path("+repr(sys.argv[2])+").touch()"
subprocess.Popen(['/usr/bin/python3','-c',child]);time.sleep(10)`
	cmd := exec.CommandContext(ctx, "/usr/bin/python3", "-c", code, ready, late)
	configureScheduleCommand(cmd)
	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("child did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("canceled helper succeeded")
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := os.Stat(late); !os.IsNotExist(err) {
		t.Fatal("owned child survived cancellation", err)
	}
}
