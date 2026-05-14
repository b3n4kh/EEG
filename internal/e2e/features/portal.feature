Feature: Participant portal journeys

  Scenario: Admin uploads an XLSX workbook through the UI
    Given a fresh portal
    And an admin user "admin" with password "admin12345"
    When I log in as "admin" with password "admin12345"
    And I upload a workbook through the admin UI
    Then the response contains "XLSX Import abgeschlossen"
    And meter "AT001" has 1 metric label

  Scenario: Participant sees only assigned meter data
    Given a fresh portal
    And participant "teilnehmer" with password "secret12345" is assigned to "AT001"
    And participant dashboard data exists for assigned, unassigned, and total meters
    When I log in as "teilnehmer" with password "secret12345"
    And I open "/"
    Then the response contains "50.000 kWh"
    And the response contains "200.000 kWh"
    And the response does not contain "700.000 kWh"
    And the response does not contain "999.000 kWh"
    When I open "/meters/AT002"
    Then the response status is 404
    When I open "/meters/TOTAL"
    Then the response status is 404

  Scenario: Bearer-token XLSX upload is idempotent
    Given a fresh portal
    And an API token "dev-token"
    When I upload the same workbook through the import API twice
    Then the first API import inserted 1 measurements
    And the second API import skipped 1 measurements

  Scenario: EDA API import creates participant access and enforces password change
    Given a fresh portal with a fake EDA API
    And an API token "dev-token"
    When I import EDA data through the API twice
    Then the first EDA import inserted 13 measurements
    And the second EDA import skipped 13 measurements
    And EDA participant "petra.akhras" has 2 assigned meters and must change password
    When I set EDA participant "petra.akhras" password to "initial12345"
    And I log in as "petra.akhras" with password "initial12345"
    Then the response contains "Passwort ändern"
    When I change the password from "initial12345" to "changed12345"
    Then the response contains "Dashboard"
    And the response contains "10.000 kWh"
    And the response does not contain "TOTAL"
