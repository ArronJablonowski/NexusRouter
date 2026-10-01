package remotecli

import (
	"flag"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"io"
)

func serviceTemplate(args []string, output, diagnostic io.Writer) error {
	var s remote.ServiceTemplateSpec
	f := flag.NewFlagSet("nexus remote service-template", flag.ContinueOnError)
	f.SetOutput(diagnostic)
	f.StringVar(&s.Platform, "platform", "", "launchd or systemd (per-user service)")
	f.StringVar(&s.Executable, "executable", "", "absolute path to the main nexus executable")
	f.StringVar(&s.WorkingDirectory, "working-directory", "", "explicit absolute runtime working directory")
	f.StringVar(&s.OwnerDirectory, "owner-directory", "", "absolute shared process admission directory used by local NexusRouter processes")
	f.StringVar(&s.Instance, "instance", "", "remote instance ID")
	f.StringVar(&s.Listen, "listen", "", "explicit listener IP:port")
	f.StringVar(&s.Config, "config", "", "absolute dedicated runtime config path")
	f.StringVar(&s.Journal, "journal", "", "absolute private remote journal directory")
	f.StringVar(&s.Trust, "trust", "", "absolute private peer registry path")
	f.StringVar(&s.Certificate, "cert", "", "absolute PEM certificate path")
	f.StringVar(&s.Key, "key", "", "absolute private PEM key path")
	f.StringVar(&s.CA, "ca", "", "absolute trusted CA PEM path")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return remote.ErrInvalid
	}
	body, err := remote.RenderServiceTemplate(s)
	if err != nil {
		return err
	}
	_, err = output.Write(body)
	return err
}
