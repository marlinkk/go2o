/**
 * Copyright 2014 @ z3q.net.
 * name :
 * author : jarryliu
 * date : 2014-01-08 21:01
 * description :
 * history :
 */

package daemon

import (
	"flag"
	"fmt"
	"github.com/garyburd/redigo/redis"
	"github.com/jsix/gof"
	"github.com/jsix/gof/db"
	"github.com/jsix/gof/db/orm"
	"github.com/robfig/cron"
	"go2o/core"
	"go2o/core/domain/interface/enum"
	"go2o/core/domain/interface/member"
	"go2o/core/domain/interface/mss"
	"go2o/core/domain/interface/order"
	"go2o/core/domain/interface/payment"
	"go2o/core/service/dps"
	"go2o/core/variable"
	"log"
	"strings"
	"time"
)

// 守护进程执行的函数
type Func func(gof.App)

// 守护进程服务
type Service interface {
	// 服务名称
	Name() string

	// 启动服务,并传入APP上下文对象
	Start(gof.App)

	// 处理订单,需根据订单不同的状态,作不同的业务,返回布尔值,如果返回false,则不继续执行
	OrderObs(*order.SubOrder) bool

	// 监视会员修改,@create:是否为新注册会员,返回布尔值,如果返回false,则不继续执行
	MemberObs(m *member.Member, create bool) bool

	// 通知支付单完成队列,返回布尔值,如果返回false,则不继续执行
	PaymentOrderObs(order *payment.PaymentOrderBean) bool

	// 处理邮件队列,返回布尔值,如果返回false,则不继续执行
	HandleMailQueue([]*mss.MailTask) bool
}

var (
	appCtx           *core.MainApp
	_db              db.Connector
	_orm             orm.Orm
	services         []Service      = make([]Service, 0)
	serviceNames     map[string]int = make(map[string]int)
	tickerDuration   time.Duration  = 20 * time.Second // 间隔20秒执行
	tickerInvokeFunc []Func         = []Func{}
	cronTab          *cron.Cron     = cron.New()
	ticker           *time.Ticker   = time.NewTicker(tickerDuration)
)

// 注册服务
func RegisterService(s Service) {
	if s == nil {
		panic("service is nil")
	}
	name := s.Name()
	if _, ok := serviceNames[name]; ok {
		panic("service named " + name + " is registed!")
	}
	serviceNames[name] = len(services)
	services = append(services, s)
}

// 添加定时执行任务(默认5秒)
func AddTickerFunc(f Func) {
	tickerInvokeFunc = append(tickerInvokeFunc, f)
}

// 启动守护进程
func Start() {
	defer func() {
		cronTab.Stop()
		ticker.Stop()
	}()

	//运行自定义服务
	for i, s := range services {
		log.Println("** [ Go2o][ Daemon] - (", i, ")", s.Name(), "daemon running")
		go s.Start(appCtx)
	}

	startCronTab() // 运行计划任务
	startTicker()  // 阻塞
}

func startTicker() {
	// 执行定时任务
	for {
		select {
		case <-ticker.C:
			for _, f := range tickerInvokeFunc {
				go f(appCtx)
			}
		}
	}
}

// 运行定时任务
func startCronTab() {
	//个人金融结算,每天2点更新数据
	cronTab.AddFunc("0 0 1 * * *", personFinanceSettle)
	//检查订单过期,5分钟检测一次
	cronTab.AddFunc("5 * * * * *", detectOrderExpires)
	cronTab.Start()
}

func recoverDaemon() {
}

type defaultService struct {
	app     gof.App
	sOrder  bool
	sMember bool
	sMail   bool
}

// 注册系统服务
func (d *defaultService) init() {
	if len(services) == 0 {
		RegisterService(d)
	} else {
		services = append([]Service{d}, services...)
	}
}

// 服务名称
func (d *defaultService) Name() string {
	return "sys"
}

// 启动服务
func (d *defaultService) Start(a gof.App) {
	d.app = a
	go superviseMemberUpdate(services)
	go superviseOrder(services)
	go supervisePaymentOrderFinish(services)
	go startMailQueue(services)
	go personFinanceSettle() //启动时结算
}

// 处理订单,需根据订单不同的状态,作不同的业务
// 返回布尔值,如果返回false,则不继续执行
func (d *defaultService) OrderObs(o *order.SubOrder) bool {
	defer Recover()
	conn := core.GetRedisConn()
	defer conn.Close()
	if d.app.Debug() {
		d.app.Log().Println("---订单", o.OrderNo, "状态:", o.State)
	}

	if d.sOrder {
		if o.State == enum.ORDER_WAIT_CONFIRM {
			//确认订单
			dps.ShoppingService.ConfirmOrder(o.Id)
		}
		d.updateOrderExpires(conn, o)
	}
	return true
}

// 监视会员修改,@create:是否为新注册会员
// 返回布尔值,如果返回false,则不继续执行
func (d *defaultService) MemberObs(m *member.Member, create bool) bool {
	defer Recover()
	if d.sMember {
		//todo: 执行会员逻辑
	}
	return true
}

// 通知支付单完成队列,返回布尔值,如果返回false,则不继续执行
func (d *defaultService) PaymentOrderObs(order *payment.PaymentOrderBean) bool {
	if d.app.Debug() {
		d.app.Log().Println("---支付单", order.TradeNo, "支付完成")
	}
	return true
}

//设置订单过期时间
func (d *defaultService) updateOrderExpires(conn redis.Conn, o *order.SubOrder) {
	if o.State == order.StatAwaitingPayment {
		//订单刚创建时,设置过期时间
		ss := dps.BaseService.GetGlobMchSaleConf()
		unix := o.UpdateTime + int64(ss.OrderTimeOutMinute)*60
		conn.Do("SET", d.getExpiresKey(o), unix)
	} else if o.State == enum.ORDER_WAIT_CONFIRM {
		//删除过期时间
		conn.Do("DEL", d.getExpiresKey(o))
	}
}
func (d *defaultService) getExpiresKey(o *order.SubOrder) string {
	return fmt.Sprintf("%s%d", variable.KvOrderExpiresTime, o.Id)
}

// 处理邮件队列
// 返回布尔值,如果返回false,则不继续执行
func (d *defaultService) HandleMailQueue(list []*mss.MailTask) bool {
	defer Recover()
	if !d.sMail {
		handleMailQueue(list)
	}
	return true
}

// 运行
func Run(ctx gof.App) {
	if ctx != nil {
		appCtx = ctx.(*core.MainApp)
	} else {
		appCtx = core.NewMainApp("app.conf")
	}
	_db = appCtx.Db()
	_orm = _db.GetOrm()
	sMail := appCtx.Config().GetString(variable.SystemMailQueueOff) != "1" //是否关闭系统邮件队列
	//sMail := cnf.GetString(variable.)

	s := &defaultService{
		sMember: true,
		sOrder:  true,
		sMail:   sMail,
	}
	s.init()
	Start()
}

// 自定义参数运行
func FlagRun() {
	var conf string
	var debug bool
	var trace bool
	var service string
	var serviceArr []string = []string{"mail", "order"}
	var ch chan bool = make(chan bool)
	flag.StringVar(&conf, "conf", "app.conf", "")
	flag.BoolVar(&debug, "debug", true, "")
	flag.BoolVar(&trace, "trace", true, "")
	flag.StringVar(&service, "service", strings.Join(serviceArr, ","), "")

	flag.Parse()

	appCtx = core.NewMainApp(conf)
	appCtx.Init(debug, trace)
	gof.CurrentApp = appCtx

	_db = appCtx.Db()
	_orm = _db.GetOrm()

	dps.Init(appCtx)

	//todo:???
	//	if service != "all" {
	//		serviceArr = strings.Split(service, ",")
	//	}
	// RegisterByName(serviceArr)

	s := &defaultService{
		sMember: true,
		sOrder:  true,
		sMail:   true,
	}
	s.init()
	Start()

	<-ch
}
