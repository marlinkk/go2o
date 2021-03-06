/**
 * Copyright 2014 @ z3q.net.
 * name :
 * author : jarryliu
 * date : 2013-12-03 23:20
 * description :
 * history :
 */
package query

import (
	"github.com/jsix/gof"
	"github.com/jsix/gof/db"
	"github.com/jsix/gof/storage"
	"go2o/core/infrastructure"
	"go2o/core/variable"
	"regexp"
)

type MerchantQuery struct {
	db.Connector
	Storage storage.Interface
}

func NewMerchantQuery(c gof.App) *MerchantQuery {
	return &MerchantQuery{
		Connector: c.Db(),
		Storage:   c.Storage(),
	}
}

var (
	commHostRegexp *regexp.Regexp
)

func getHostRegexp() *regexp.Regexp {
	if commHostRegexp == nil {
		commHostRegexp = regexp.MustCompile("([^\\.]+)." +
			infrastructure.GetApp().Config().GetString(variable.ServerDomain))
	}
	return commHostRegexp
}

// 根据主机查询商户编号
func (m *MerchantQuery) QueryMerchantIdByHost(host string) int {
	//  $ 获取合作商ID
	// $ hostname : 域名
	// *.wdian.net  二级域名
	// www.dc1.com  顶级域名

	var merchantId int
	var err error

	reg := getHostRegexp()
	if reg.MatchString(host) {
		matches := reg.FindAllStringSubmatch(host, 1)
		usr := matches[0][1]
		err = m.Connector.ExecScalar(`SELECT id FROM mch_merchant WHERE usr=?`, &merchantId, usr)
	} else {
		err = m.Connector.ExecScalar(
			`SELECT id FROM mch_merchant INNER JOIN pt_siteconf
                     ON pt_siteconf.merchant_id = mch_merchant.id
                     WHERE host=?`, &merchantId, host)
	}
	if err != nil {
		gof.CurrentApp.Log().Error(err)
	}
	return merchantId
}

// 验证用户密码并返回编号
func (m *MerchantQuery) Verify(usr, pwd string) int {
	var id int = -1
	m.Connector.ExecScalar("SELECT id FROM mch_merchant WHERE usr=? AND pwd=?", &id, usr, pwd)
	return id
}
