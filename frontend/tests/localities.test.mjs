import assert from 'node:assert/strict'
import { test } from 'node:test'

import { localitiesOf } from '../src/pages/locations/localities.ts'

const component = (jurisdiction, name) => ({ id: `${jurisdiction}.x`, jurisdiction, name })

const coverage = {
  regimes: [
    {
      country: 'US',
      components: [
        component('jurisdiction:us-nc', 'North Carolina sales tax on telecommunications'),
        component('jurisdiction:us-nc-wake-county', 'Wake County local sales tax on equipment (North Carolina)'),
        component('jurisdiction:us-nc-wake-county', 'Wake County transit sales tax on equipment (North Carolina)'),
        component('jurisdiction:us-nc-mcdowell-county', 'McDowell County local sales tax on equipment (North Carolina)'),
        component('jurisdiction:us-nc-new-hanover-county', 'New Hanover County local sales tax on equipment (North Carolina)'),
        component('jurisdiction:us-ny-new-york-city', 'New York City sales tax'),
        component('jurisdiction:us-nd', 'North Dakota sales tax'),
      ],
    },
    { country: 'CA', components: [component('jurisdiction:ca-on', 'Ontario HST')] },
  ],
}

test('a state lists each of its counties once, named as its components name them', () => {
  assert.deepEqual(localitiesOf(coverage, 'US-NC'), [
    { id: 'jurisdiction:us-nc-mcdowell-county', name: 'McDowell County' },
    { id: 'jurisdiction:us-nc-new-hanover-county', name: 'New Hanover County' },
    { id: 'jurisdiction:us-nc-wake-county', name: 'Wake County' },
  ])
})

test('a city is named from its jurisdiction id', () => {
  assert.deepEqual(localitiesOf(coverage, 'US-NY'), [{ id: 'jurisdiction:us-ny-new-york-city', name: 'New York City' }])
})

test('the state itself and a state with no localities list nothing', () => {
  assert.deepEqual(localitiesOf(coverage, 'US-ND'), [])
  assert.deepEqual(localitiesOf(coverage, ''), [])
})
