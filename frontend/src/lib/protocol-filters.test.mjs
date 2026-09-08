import assert from 'node:assert/strict'
import test from 'node:test'
import {filterProtocolRows, protocolCategoryOptions} from './protocol-filters.js'

const rows = [
  {member_id: 'ranked', last_name: 'Петров', first_name: 'Иван', number: 10, gender: 'male', category_id: 'm', category_name: 'Мужчины'},
  {member_id: 'no-reads', last_name: 'Иванова', first_name: 'Анна', number: 150, gender: 'female', category_id: 'f', category_name: 'Женщины'},
  {member_id: 'split-only', last_name: 'Сидоров', number: null, gender: null, category_id: null},
]
const ids = result => result.map(row => row.member_id)

test('search, gender and category filters share unchanged rules across both sections', () => {
  assert.deepEqual(ids(filterProtocolRows(rows, ' иВаНоВа ', 'all', 'all')), ['no-reads'])
  assert.deepEqual(ids(filterProtocolRows(rows, '150', 'female', 'f')), ['no-reads'])
  assert.deepEqual(ids(filterProtocolRows(rows, '150', 'male', 'all')), [])
  assert.deepEqual(ids(filterProtocolRows(rows, '', 'all', 'm')), ['ranked'])
  assert.deepEqual(ids(filterProtocolRows(rows, '', 'all', 'all')), ['ranked', 'no-reads', 'split-only'])
  assert.deepEqual(ids(filterProtocolRows(rows, 'null', 'all', 'all')), [])
})

test('category options include unfinished-only groups without duplicates', () => {
  assert.deepEqual(protocolCategoryOptions([...rows, rows[1]]), [
    {value: 'f', label: 'Женщины'}, {value: 'm', label: 'Мужчины'},
  ])
})

test('filtering does not mutate official row order or assign places/times', () => {
  const snapshot = structuredClone(rows)
  filterProtocolRows(rows, '150', 'all', 'all')
  protocolCategoryOptions(rows)
  assert.deepEqual(rows, snapshot)
})
