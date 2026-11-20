import assert from 'node:assert/strict'
import test from 'node:test'
import { buildMapURL } from './mapURL.js'

// Synthetic public-map fixture only. No actual map, GPS or network service is called.
const source = Object.freeze({ longitude: 116.397428, latitude: 39.909234, coordinate_system: 'WGS84', device_id: 'NOT_SHARED', name: 'NOT_SHARED' })

test('builds official WGS84 marker URI, with longitude first and no Key', () => {
  const url = new URL(buildMapURL(source))
  assert.equal(url.origin, 'https://uri.amap.com')
  assert.equal(url.pathname, '/marker')
  assert.deepEqual(Object.fromEntries(url.searchParams), {
    position: '116.397,39.909', name: '设备大概位置', coordinate: 'wgs84', callnative: '0'
  })
  assert.equal(url.searchParams.has('key'), false)
  assert.equal(url.href.includes('NOT_SHARED'), false)
})

test('sends only three decimal places while preserving exact source coordinates', () => {
  assert.equal(new URL(buildMapURL({ ...source, longitude: 116.397678, latitude: 39.909876 })).searchParams.get('position'), '116.398,39.910')
  assert.equal(source.longitude, 116.397428)
  assert.equal(source.latitude, 39.909234)
})

test('supports zero and valid geographic bounds', () => {
  for (const [longitude, latitude, expected] of [[0, 0, '0.000,0.000'], [-180, -90, '-180.000,-90.000'], [180, 90, '180.000,90.000'], [-0.0001, -0.0001, '0.000,0.000']]) {
    assert.equal(new URL(buildMapURL({ longitude, latitude, coordinate_system: 'WGS84' })).searchParams.get('position'), expected)
  }
})

test('rejects absent, non-WGS84, nonnumeric, nonfinite and out-of-range coordinates', () => {
  for (const value of [null, undefined, {}, { ...source, coordinate_system: 'GCJ02' }, { ...source, coordinate_system: 'BD09' }, { ...source, longitude: '116.397' }, { ...source, latitude: null }, { ...source, longitude: NaN }, { ...source, latitude: Infinity }, { ...source, longitude: -180.001 }, { ...source, latitude: 90.001 }]) assert.equal(buildMapURL(value), '')
})

test('building a URL is pure and performs no external request', () => {
  const previousFetch = globalThis.fetch
  globalThis.fetch = () => { throw new Error('No network operation is permitted in this test') }
  try { assert.ok(buildMapURL(source).startsWith('https://uri.amap.com/marker?')) } finally { globalThis.fetch = previousFetch }
})
