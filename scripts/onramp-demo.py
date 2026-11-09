#!/usr/bin/env python3
"""四家本地闭环：不依赖第三方 Python 包，不打印 Bearer Token。"""
import argparse
import json
import uuid
import urllib.request

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--base-url', default='http://127.0.0.1:8098')
parser.add_argument('--provider', choices=['all', 'moonpay', 'alchemypay', 'transak', 'ramp'], default='all')
parser.add_argument('--drop-webhook', action='store_true')
args = parser.parse_args()
token = ''

def request(path, payload=None, key=None):
    headers = {'Content-Type': 'application/json'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    if key:
        headers['Idempotency-Key'] = key
    req = urllib.request.Request(args.base_url.rstrip('/') + path,
        data=None if payload is None else json.dumps(payload).encode(), headers=headers)
    with urllib.request.urlopen(req, timeout=25) as res:
        return json.load(res)

if request('/onramp/providers')['mode'] != 'demo':
    raise SystemExit('此脚本只允许本地 demo 模式，拒绝操作真实网关。')
token = request('/demo/session', {})['token']
providers = ['moonpay', 'alchemypay', 'transak', 'ramp'] if args.provider == 'all' else [args.provider]
for provider in providers:
    key = str(uuid.uuid4())
    payload = {'provider': provider, 'wallet_address': '0x1111111111111111111111111111111111111111', 'fiat_amount_minor': 3000}
    order = request('/onramp/orders', payload, key)
    replay = request('/onramp/orders', payload, key)
    assert replay['id'] == order['id'], '幂等重放产生了新订单'
    for action in ['kyc', 'pay', 'deliver']:
        order = request('/onramp/demo/' + order['id'] + '/advance', {'action': action, 'deliver_webhook': not args.drop_webhook})
        print(provider, action, order['status'])
    if args.drop_webhook:
        assert order['status'] != 'completed', '未验证供应商状态却被标记完成'
        order = request('/onramp/orders/' + order['id'] + '/sync', {})
    assert order['status'] == 'completed' and order['tx_hash'], '交付未闭环'
    print(json.dumps({'provider': provider, 'id': order['id'], 'status': order['status'], 'crypto_amount': order['crypto_amount']}, ensure_ascii=False))
