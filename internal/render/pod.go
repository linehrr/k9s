package render

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/tview"
	"github.com/gdamore/tcell"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	mv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

// Pod renders a K8s Pod to screen.
type Pod struct{}

// ColorerFunc colors a resource row.
func (p Pod) ColorerFunc() ColorerFunc {
	return func(ns string, h Header, re RowEvent) tcell.Color {
		c := DefaultColorer(ns, h, re)

		statusCol := h.IndexOf("STATUS", true)
		if statusCol == -1 {
			return c
		}
		status := strings.TrimSpace(re.Row.Fields[statusCol])
		switch status {
		case Pending:
			c = PendingColor
		case ContainerCreating, PodInitializing:
			c = AddColor
		case Initialized:
			c = HighlightColor
		case Completed:
			c = CompletedColor
		case Running:
			c = StdColor
			if !Happy(ns, h, re.Row) {
				c = ErrColor
			}
		case Terminating:
			c = KillColor
		default:
			if !Happy(ns, h, re.Row) {
				c = ErrColor
			}
		}
		return c
	}
}

// Header returns a header row.
func (Pod) Header(ns string) Header {
	return Header{
		HeaderColumn{Name: "NAMESPACE"},
		HeaderColumn{Name: "NAME"},
		HeaderColumn{Name: "PF"},
		HeaderColumn{Name: "READY"},
		HeaderColumn{Name: "RESTARTS", Align: tview.AlignRight},
		HeaderColumn{Name: "STATUS"},
		HeaderColumn{Name: "CPU(R:L)", Align: tview.AlignRight, MX: true, Wide: true},
		HeaderColumn{Name: "MEM(R:L)", Align: tview.AlignRight, MX: true, Wide: true},
		HeaderColumn{Name: "CPU", Align: tview.AlignRight, MX: true},
		HeaderColumn{Name: "MEM", Align: tview.AlignRight, MX: true},
		HeaderColumn{Name: "%CPU/R", Align: tview.AlignRight, MX: true},
		HeaderColumn{Name: "%MEM/R", Align: tview.AlignRight, MX: true},
		HeaderColumn{Name: "%CPU/L", Align: tview.AlignRight, MX: true},
		HeaderColumn{Name: "%MEM/L", Align: tview.AlignRight, MX: true},
		HeaderColumn{Name: "IP"},
		HeaderColumn{Name: "NODE"},
		HeaderColumn{Name: "QOS", Wide: true},
		HeaderColumn{Name: "LABELS", Wide: true},
		HeaderColumn{Name: "VALID", Wide: true},
		HeaderColumn{Name: "AGE", Time: true, Decorator: AgeDecorator},
	}
}

// Render renders a K8s resource to screen.
func (p Pod) Render(o interface{}, ns string, r *Row) error {
	pwm, ok := o.(*PodWithMetrics)
	if !ok {
		return fmt.Errorf("Expected PodWithMetrics, but got %T", o)
	}

	var po v1.Pod
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(pwm.Raw.Object, &po); err != nil {
		return err
	}

	ss := po.Status.ContainerStatuses
	cr, _, rc := p.Statuses(ss)
	c, perc, res := p.gatherPodMX(&po, pwm.MX)
	phase := p.Phase(&po)
	r.ID = client.MetaFQN(po.ObjectMeta)
	r.Fields = Fields{
		po.Namespace,
		po.ObjectMeta.Name,
		"●",
		strconv.Itoa(cr) + "/" + strconv.Itoa(len(ss)),
		strconv.Itoa(rc),
		phase,
		ToResourcesMc(res),
		ToResourcesMi(res),
		c.cpu,
		c.mem,
		perc.cpu,
		perc.mem,
		perc.cpuLim,
		perc.memLim,
		na(po.Status.PodIP),
		na(po.Spec.NodeName),
		p.mapQOS(po.Status.QOSClass),
		mapToStr(po.Labels),
		asStatus(p.diagnose(phase, cr, len(ss))),
		toAge(po.ObjectMeta.CreationTimestamp),
	}

	return nil
}

func (p Pod) diagnose(phase string, cr, ct int) error {
	if phase == Completed {
		return nil
	}
	if cr != ct || ct == 0 {
		return fmt.Errorf("container ready check failed: %d of %d", cr, ct)
	}

	return nil
}

// ----------------------------------------------------------------------------
// Helpers...

// PodWithMetrics represents a pod and its metrics.
type PodWithMetrics struct {
	Raw *unstructured.Unstructured
	MX  *mv1beta1.PodMetrics
}

// GetObjectKind returns a schema object.
func (p *PodWithMetrics) GetObjectKind() schema.ObjectKind {
	return nil
}

// DeepCopyObject returns a container copy.
func (p *PodWithMetrics) DeepCopyObject() runtime.Object {
	return p
}

const (
	requestCPU qualifiedResource = "rcpu"
	requestMEM                   = "rmem"
	limitCPU                     = "lcpu"
	limitMEM                     = "lmem"
)

type (
	qualifiedResource string
	resources         map[qualifiedResource]*resource.Quantity
)

func (*Pod) gatherPodMX(pod *v1.Pod, mx *mv1beta1.PodMetrics) (c, p metric, r resources) {
	c, p = noMetric(), noMetric()
	if mx == nil {
		return
	}

	coMetrix := make(map[string]v1.ResourceList)
	for _, cm := range mx.Containers {
		coMetrix[cm.Name] = cm.Usage
	}
	cpu, mem := currentRes(mx)
	c = metric{
		cpu: ToMc(cpu.MilliValue()),
		mem: ToMi(client.ToMB(mem.Value())),
	}

	rc, rm := podRequests(pod.Spec)
	lc, lm := podLimits(pod.Spec)
	r = make(resources, 4)
	r[requestCPU], r[requestMEM] = rc, rm
	r[limitCPU], r[limitMEM] = lc, lm
	p = metric{
		cpu:    client.ToPercentageStr(cpu.MilliValue(), rc.MilliValue()),
		mem:    client.ToPercentageStr(client.ToMB(mem.Value()), client.ToMB(rm.Value())),
		cpuLim: client.ToPercentageStr(cpu.MilliValue(), lc.MilliValue()),
		memLim: client.ToPercentageStr(client.ToMB(mem.Value()), client.ToMB(lm.Value())),
	}

	return
}

func containerResources(co v1.Container) (cpu, mem *resource.Quantity) {
	req, limit := co.Resources.Requests, co.Resources.Limits
	switch {
	case len(req) != 0:
		cpu, mem = req.Cpu(), req.Memory()
	case len(limit) != 0:
		cpu, mem = limit.Cpu(), limit.Memory()
	}

	return
}

func containerLimits(co v1.Container) (cpu, mem *resource.Quantity) {
	limit := co.Resources.Limits
	if len(limit) == 0 {
		return nil, nil
	}
	return limit.Cpu(), limit.Memory()
}

func resourceLimits(cc []v1.Container) (cpu, mem *resource.Quantity) {
	cpu, mem = new(resource.Quantity), new(resource.Quantity)
	for _, co := range cc {
		limit := co.Resources.Limits
		if len(limit) == 0 {
			cpu.Reset()
			mem.Reset()
			break
		}
		if limit.Cpu() != nil {
			cpu.Add(*limit.Cpu())
		}
		if limit.Memory() != nil {
			mem.Add(*limit.Memory())
		}
	}
	return
}

func podLimits(spec v1.PodSpec) (*resource.Quantity, *resource.Quantity) {
	cc, cm := resourceLimits(spec.Containers)
	ic, im := resourceLimits(spec.InitContainers)

	cc.Add(*ic)
	cm.Add(*im)

	return cc, cm
}

func podRequests(spec v1.PodSpec) (*resource.Quantity, *resource.Quantity) {
	cc, cm := resourceRequests(spec.Containers)
	ic, im := resourceRequests(spec.InitContainers)

	cc.Add(*ic)
	cm.Add(*im)

	return cc, cm
}

func resourceRequests(cc []v1.Container) (cpu, mem *resource.Quantity) {
	cpu, mem = new(resource.Quantity), new(resource.Quantity)
	for _, co := range cc {
		c, m := containerResources(co)
		if c == nil || m == nil {
			cpu.Reset()
			mem.Reset()
			break
		}
		if c != nil {
			cpu.Add(*c)
		}
		if m != nil {
			mem.Add(*m)
		}
	}

	return
}

func currentRes(mx *mv1beta1.PodMetrics) (cpu, mem resource.Quantity) {
	if mx == nil {
		return
	}
	for _, co := range mx.Containers {
		c, m := co.Usage.Cpu(), co.Usage.Memory()
		cpu.Add(*c)
		mem.Add(*m)
	}
	return
}

func (*Pod) mapQOS(class v1.PodQOSClass) string {
	switch class {
	case v1.PodQOSGuaranteed:
		return "GA"
	case v1.PodQOSBurstable:
		return "BU"
	default:
		return "BE"
	}
}

// Statuses reports current pod container statuses.
func (*Pod) Statuses(ss []v1.ContainerStatus) (cr, ct, rc int) {
	for _, c := range ss {
		if c.State.Terminated != nil {
			ct++
		}
		if c.Ready {
			cr = cr + 1
		}
		rc += int(c.RestartCount)
	}

	return
}

// Phase reports the given pod phase.
func (p *Pod) Phase(po *v1.Pod) string {
	status := string(po.Status.Phase)
	if po.Status.Reason != "" {
		if po.DeletionTimestamp != nil && po.Status.Reason == "NodeLost" {
			return "Unknown"
		}
		status = po.Status.Reason
	}

	status, ok := p.initContainerPhase(po.Status, len(po.Spec.InitContainers), status)
	if ok {
		return status
	}

	status, ok = p.containerPhase(po.Status, status)
	if ok && status == Completed {
		status = Running
	}
	if po.DeletionTimestamp == nil {
		return status
	}

	return Terminating
}

func (*Pod) containerPhase(st v1.PodStatus, status string) (string, bool) {
	var running bool
	for i := len(st.ContainerStatuses) - 1; i >= 0; i-- {
		cs := st.ContainerStatuses[i]
		switch {
		case cs.State.Waiting != nil && cs.State.Waiting.Reason != "":
			status = cs.State.Waiting.Reason
		case cs.State.Terminated != nil && cs.State.Terminated.Reason != "":
			status = cs.State.Terminated.Reason
		case cs.State.Terminated != nil:
			if cs.State.Terminated.Signal != 0 {
				status = "Signal:" + strconv.Itoa(int(cs.State.Terminated.Signal))
			} else {
				status = "ExitCode:" + strconv.Itoa(int(cs.State.Terminated.ExitCode))
			}
		case cs.Ready && cs.State.Running != nil:
			running = true
		}
	}

	return status, running
}

func (*Pod) initContainerPhase(st v1.PodStatus, initCount int, status string) (string, bool) {
	for i, cs := range st.InitContainerStatuses {
		s := checkContainerStatus(cs, i, initCount)
		if s == "" {
			continue
		}
		return s, true
	}

	return status, false
}

// ----------------------------------------------------------------------------
// Helpers..

func checkContainerStatus(cs v1.ContainerStatus, i, initCount int) string {
	switch {
	case cs.State.Terminated != nil:
		if cs.State.Terminated.ExitCode == 0 {
			return ""
		}
		if cs.State.Terminated.Reason != "" {
			return "Init:" + cs.State.Terminated.Reason
		}
		if cs.State.Terminated.Signal != 0 {
			return "Init:Signal:" + strconv.Itoa(int(cs.State.Terminated.Signal))
		}
		return "Init:ExitCode:" + strconv.Itoa(int(cs.State.Terminated.ExitCode))
	case cs.State.Waiting != nil && cs.State.Waiting.Reason != "" && cs.State.Waiting.Reason != "PodInitializing":
		return "Init:" + cs.State.Waiting.Reason
	default:
		return "Init:" + strconv.Itoa(i) + "/" + strconv.Itoa(initCount)
	}
}
