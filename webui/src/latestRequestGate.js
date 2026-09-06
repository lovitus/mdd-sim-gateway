export function createLatestRequestGate() {
  let generation = 0
  let route = ''
  return {
    select(nextRoute) {
      route = String(nextRoute || '')
      generation += 1
    },
    begin(expectedRoute) {
      generation += 1
      return { generation, route: String(expectedRoute || '') }
    },
    accepts(token) {
      return token?.generation === generation && token.route === route
    },
  }
}
