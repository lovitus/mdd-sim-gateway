import countries from '../../go-runtime/internal/linecatalog/mcc_country.json' with { type: 'json' }

// Same legacy MDD table and padding as linecatalog.CountryForMCC. Not roaming location.
export function simHomeCountry(mcc) {
  return countries[String(mcc ?? '').trim().padStart(3, '0')] || ''
}
