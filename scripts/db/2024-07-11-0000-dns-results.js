exports.up = function(knex) {
    return knex.schema.table('monitor', function(table) {
      table.string('dns_last_result', 2000).alter();
    });
  };

  exports.down = function(knex) {
    return knex.schema.table('monitor', function(table) {
      table.string('dns_last_result', 255).alter();
    });
  };
