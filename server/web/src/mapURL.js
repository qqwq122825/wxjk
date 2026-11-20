// Official single-marker URI: https://lbs.amap.com/api/uri-api/gettingstarted
// This only builds a link; it never loads a SDK, requests location or calls a service.
export function buildMapURL(location) {
  if (!location || location.coordinate_system !== 'WGS84' ||
    typeof location.longitude !== 'number' || !Number.isFinite(location.longitude) ||
    typeof location.latitude !== 'number' || !Number.isFinite(location.latitude) ||
    location.longitude < -180 || location.longitude > 180 ||
    location.latitude < -90 || location.latitude > 90) return ''

  // Send an approximate position only. Do not expose the exact source coordinates,
  // device name/ID, report identity or ownership information in the external URL.
  const approximate = value => Number(value.toFixed(3)).toFixed(3)
  const url = new URL('https://uri.amap.com/marker')
  url.search = new URLSearchParams({
    position: `${approximate(location.longitude)},${approximate(location.latitude)}`,
    name: '设备大概位置',
    coordinate: 'wgs84',
    callnative: '0'
  }).toString()
  return url.href
}
