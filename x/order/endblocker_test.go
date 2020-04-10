package order

import (
	"testing"

	"github.com/cosmos/cosmos-sdk/x/supply"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth"
	"github.com/stretchr/testify/require"
	abci "github.com/tendermint/tendermint/abci/types"

	"github.com/okex/okchain/x/common"
	"github.com/okex/okchain/x/dex"
	"github.com/okex/okchain/x/order/types"
	token "github.com/okex/okchain/x/token/types"
)

func TestEndBlockerPeriodicMatch(t *testing.T) {
	mapp, addrKeysSlice := getMockApp(t, 2)
	k := mapp.orderKeeper
	mapp.BeginBlock(abci.RequestBeginBlock{Header: abci.Header{Height: 2}})

	var startHeight int64 = 10
	ctx := mapp.BaseApp.NewContext(false, abci.Header{}).WithBlockHeight(startHeight)
	mapp.supplyKeeper.SetSupply(ctx, supply.NewSupply(mapp.TotalCoinsSupply))

	feeParams := types.DefaultParams()
	mapp.orderKeeper.SetParams(ctx, &feeParams)

	tokenPair := dex.GetBuiltInTokenPair()
	err := mapp.dexKeeper.SaveTokenPair(ctx, tokenPair)
	require.Nil(t, err)

	// mock orders
	orders := []*types.Order{
		types.MockOrder(types.FormatOrderID(startHeight, 1), types.TestTokenPair, types.BuyOrder, "10.0", "1.0"),
		types.MockOrder(types.FormatOrderID(startHeight, 2), types.TestTokenPair, types.SellOrder, "10.0", "0.5"),
		types.MockOrder(types.FormatOrderID(startHeight, 3), types.TestTokenPair, types.SellOrder, "10.0", "2.5"),
	}
	orders[0].Sender = addrKeysSlice[0].Address
	orders[1].Sender = addrKeysSlice[1].Address
	orders[2].Sender = addrKeysSlice[1].Address
	for i := 0; i < 3; i++ {
		err := k.PlaceOrder(ctx, orders[i])
		require.NoError(t, err)
	}
	// subtract all okb of addr0
	// 100 - 10 - 0.2592
	err = k.LockCoins(ctx, addrKeysSlice[0].Address, sdk.DecCoins{{Denom: common.NativeToken,
		Amount: sdk.MustNewDecFromStr("89.7408")}}, token.LockCoinsTypeQuantity)
	require.NoError(t, err)

	// call EndBlocker to execute periodic match
	EndBlocker(ctx, k)

	// check order status
	order0 := k.GetOrder(ctx, orders[0].OrderID)
	order1 := k.GetOrder(ctx, orders[1].OrderID)
	order2 := k.GetOrder(ctx, orders[2].OrderID)
	require.EqualValues(t, types.OrderStatusFilled, order0.Status)
	require.EqualValues(t, types.OrderStatusFilled, order1.Status)
	require.EqualValues(t, types.OrderStatusOpen, order2.Status)
	require.EqualValues(t, sdk.MustNewDecFromStr("2"), order2.RemainQuantity)

	// check depth book
	depthBook := k.GetDepthBookCopy(types.TestTokenPair)
	require.EqualValues(t, 1, len(depthBook.Items))
	require.EqualValues(t, sdk.MustNewDecFromStr("10.0"), depthBook.Items[0].Price)
	require.True(sdk.DecEq(t, sdk.ZeroDec(), depthBook.Items[0].BuyQuantity))
	require.EqualValues(t, sdk.MustNewDecFromStr("2"), depthBook.Items[0].SellQuantity)

	depthBookDB := k.GetDepthBookFromDB(ctx, types.TestTokenPair)
	require.EqualValues(t, 1, len(depthBookDB.Items))
	require.EqualValues(t, sdk.MustNewDecFromStr("10.0"), depthBookDB.Items[0].Price)
	require.True(sdk.DecEq(t, sdk.ZeroDec(), depthBookDB.Items[0].BuyQuantity))
	require.EqualValues(t, sdk.MustNewDecFromStr("2"), depthBookDB.Items[0].SellQuantity)

	// check product price - order ids
	key := types.FormatOrderIDsKey(types.TestTokenPair, sdk.MustNewDecFromStr("10.0"), types.SellOrder)
	orderIDs := k.GetProductPriceOrderIDs(key)
	require.EqualValues(t, 1, len(orderIDs))
	require.EqualValues(t, order2.OrderID, orderIDs[0])
	key = types.FormatOrderIDsKey(types.TestTokenPair, sdk.MustNewDecFromStr("10.0"), types.BuyOrder)
	orderIDs = k.GetProductPriceOrderIDs(key)
	require.EqualValues(t, 0, len(orderIDs))

	// check block match result
	result := k.GetBlockMatchResult()
	require.EqualValues(t, sdk.MustNewDecFromStr("10.0"), result.ResultMap[types.TestTokenPair].Price)
	require.EqualValues(t, sdk.MustNewDecFromStr("1.0"), result.ResultMap[types.TestTokenPair].Quantity)
	require.EqualValues(t, 3, len(result.ResultMap[types.TestTokenPair].Deals))
	require.EqualValues(t, order0.OrderID, result.ResultMap[types.TestTokenPair].Deals[0].OrderID)
	require.EqualValues(t, order1.OrderID, result.ResultMap[types.TestTokenPair].Deals[1].OrderID)
	require.EqualValues(t, order2.OrderID, result.ResultMap[types.TestTokenPair].Deals[2].OrderID)
	// check closed order id
	closedOrderIDs := k.GetLastClosedOrderIDs(ctx)
	require.Equal(t, 2, len(closedOrderIDs))
	require.Equal(t, orders[0].OrderID, closedOrderIDs[0])
	require.Equal(t, orders[1].OrderID, closedOrderIDs[1])

	// check account balance
	acc0 := mapp.AccountKeeper.GetAccount(ctx, addrKeysSlice[0].Address)
	acc1 := mapp.AccountKeeper.GetAccount(ctx, addrKeysSlice[1].Address)
	expectCoins0 := sdk.DecCoins{
		sdk.NewDecCoinFromDec(common.NativeToken, sdk.MustNewDecFromStr("0.2592")),
		sdk.NewDecCoinFromDec(common.TestToken, sdk.MustNewDecFromStr("100.999")), // 100 + 1 * (1 - 0.001)
	}
	expectCoins1 := sdk.DecCoins{
		sdk.NewDecCoinFromDec(common.NativeToken, sdk.MustNewDecFromStr("109.7308")), // 100 + 10 * (1-0.001) - 0.2592
		sdk.NewDecCoinFromDec(common.TestToken, sdk.MustNewDecFromStr("97")),         // 100 - 0.5 - 2.5
	}
	require.EqualValues(t, expectCoins0.String(), acc0.GetCoins().String())
	require.EqualValues(t, expectCoins1.String(), acc1.GetCoins().String())

	// check fee pool
	feeCollector := mapp.supplyKeeper.GetModuleAccount(ctx, auth.FeeCollectorName)
	collectedFees := feeCollector.GetCoins()
	require.EqualValues(t, "", collectedFees.String())
}

func TestEndBlockerPeriodicMatchBusyProduct(t *testing.T) {
	mapp, addrKeysSlice := getMockApp(t, 2)
	k := mapp.orderKeeper
	mapp.BeginBlock(abci.RequestBeginBlock{Header: abci.Header{Height: 2}})
	ctx := mapp.BaseApp.NewContext(false, abci.Header{}).WithBlockHeight(10)
	mapp.supplyKeeper.SetSupply(ctx, supply.NewSupply(mapp.TotalCoinsSupply))
	feeParams := types.DefaultParams()
	feeParams.MaxDealsPerBlock = 2
	k.SetParams(ctx, &feeParams)

	tokenPair := dex.GetBuiltInTokenPair()
	err := mapp.dexKeeper.SaveTokenPair(ctx, tokenPair)
	require.Nil(t, err)

	// mock orders
	orders := []*types.Order{
		types.MockOrder("", types.TestTokenPair, types.BuyOrder, "10.0", "1.0"),
		types.MockOrder("", types.TestTokenPair, types.SellOrder, "10.0", "0.5"),
		types.MockOrder("", types.TestTokenPair, types.SellOrder, "10.0", "2.5"),
	}
	orders[0].Sender = addrKeysSlice[0].Address
	orders[1].Sender = addrKeysSlice[1].Address
	orders[2].Sender = addrKeysSlice[1].Address
	for i := 0; i < 3; i++ {
		err := k.PlaceOrder(ctx, orders[i])
		require.NoError(t, err)
	}

	// ------- call EndBlocker at height 10 -------//
	EndBlocker(ctx, k)

	// check product lock
	lock := k.GetDexKeeper().GetLockedProductsCopy().Data[types.TestTokenPair]
	require.NotNil(t, lock)
	require.EqualValues(t, 10, lock.BlockHeight)
	require.EqualValues(t, sdk.MustNewDecFromStr("10.0"), lock.Price)
	require.EqualValues(t, sdk.MustNewDecFromStr("1.0"), lock.Quantity)
	require.EqualValues(t, sdk.MustNewDecFromStr("1.0"), lock.BuyExecuted)
	require.EqualValues(t, sdk.MustNewDecFromStr("0.5"), lock.SellExecuted)

	// check order status
	order0 := k.GetOrder(ctx, orders[0].OrderID)
	order1 := k.GetOrder(ctx, orders[1].OrderID)
	order2 := k.GetOrder(ctx, orders[2].OrderID)
	require.EqualValues(t, types.OrderStatusFilled, order0.Status)
	require.EqualValues(t, types.OrderStatusFilled, order1.Status)
	require.EqualValues(t, types.OrderStatusOpen, order2.Status)
	require.EqualValues(t, sdk.MustNewDecFromStr("2.5"), order2.RemainQuantity)

	// check depth book
	depthBook := k.GetDepthBookCopy(types.TestTokenPair)
	require.EqualValues(t, 1, len(depthBook.Items))
	require.EqualValues(t, sdk.MustNewDecFromStr("10.0"), depthBook.Items[0].Price)
	require.True(sdk.DecEq(t, sdk.ZeroDec(), depthBook.Items[0].BuyQuantity))
	require.EqualValues(t, sdk.MustNewDecFromStr("2.5"), depthBook.Items[0].SellQuantity)

	// check product price - order ids
	key := types.FormatOrderIDsKey(types.TestTokenPair, sdk.MustNewDecFromStr("10.0"), types.SellOrder)
	orderIDs := k.GetProductPriceOrderIDs(key)
	require.EqualValues(t, 1, len(orderIDs))
	require.EqualValues(t, order2.OrderID, orderIDs[0])
	key = types.FormatOrderIDsKey(types.TestTokenPair, sdk.MustNewDecFromStr("10.0"), types.BuyOrder)
	orderIDs = k.GetProductPriceOrderIDs(key)
	require.EqualValues(t, 0, len(orderIDs))

	// check block match result
	result := k.GetBlockMatchResult()
	require.EqualValues(t, 10, result.ResultMap[types.TestTokenPair].BlockHeight)
	require.EqualValues(t, sdk.MustNewDecFromStr("10.0"), result.ResultMap[types.TestTokenPair].Price)
	require.EqualValues(t, sdk.MustNewDecFromStr("1.0"), result.ResultMap[types.TestTokenPair].Quantity)
	require.EqualValues(t, 2, len(result.ResultMap[types.TestTokenPair].Deals))
	require.EqualValues(t, order0.OrderID, result.ResultMap[types.TestTokenPair].Deals[0].OrderID)
	require.EqualValues(t, order1.OrderID, result.ResultMap[types.TestTokenPair].Deals[1].OrderID)

	// check account balance
	acc0 := mapp.AccountKeeper.GetAccount(ctx, addrKeysSlice[0].Address)
	acc1 := mapp.AccountKeeper.GetAccount(ctx, addrKeysSlice[1].Address)
	expectCoins0 := sdk.DecCoins{
		sdk.NewDecCoinFromDec(common.NativeToken, sdk.MustNewDecFromStr("90")),    // 100 - 10
		sdk.NewDecCoinFromDec(common.TestToken, sdk.MustNewDecFromStr("100.999")), // 100 + 1 * (1 - 0.001)
	}
	expectCoins1 := sdk.DecCoins{
		sdk.NewDecCoinFromDec(common.NativeToken, sdk.MustNewDecFromStr("104.7358")), // 100 + 5 * (1 - 0.001) - 0.2592
		sdk.NewDecCoinFromDec(common.TestToken, sdk.MustNewDecFromStr("97")),         // 100 - 0.5 - 2.5
	}

	require.EqualValues(t, expectCoins0.String(), acc0.GetCoins().String())
	require.EqualValues(t, expectCoins1.String(), acc1.GetCoins().String())

	// ------- call EndBlock at height 11, continue filling ------- //
	ctx = mapp.BaseApp.NewContext(false, abci.Header{}).WithBlockHeight(11)
	BeginBlocker(ctx, k)
	EndBlocker(ctx, k)

	// check product lock
	lock = k.GetDexKeeper().GetLockedProductsCopy().Data[types.TestTokenPair]
	require.Nil(t, lock)

	// check order status
	order2 = k.GetOrder(ctx, orders[2].OrderID)
	require.EqualValues(t, types.OrderStatusOpen, order2.Status)
	require.EqualValues(t, sdk.MustNewDecFromStr("2.0"), order2.RemainQuantity)

	// check depth book
	depthBook = k.GetDepthBookCopy(types.TestTokenPair)
	require.EqualValues(t, sdk.MustNewDecFromStr("2.0"), depthBook.Items[0].SellQuantity)

	// check block match result
	result = k.GetBlockMatchResult()
	require.EqualValues(t, 10, result.ResultMap[types.TestTokenPair].BlockHeight)
	require.EqualValues(t, sdk.MustNewDecFromStr("10.0"), result.ResultMap[types.TestTokenPair].Price)
	require.EqualValues(t, sdk.MustNewDecFromStr("1.0"), result.ResultMap[types.TestTokenPair].Quantity)
	require.EqualValues(t, 1, len(result.ResultMap[types.TestTokenPair].Deals))
	require.EqualValues(t, order2.OrderID, result.ResultMap[types.TestTokenPair].Deals[0].OrderID)

	// check account balance
	acc0 = mapp.AccountKeeper.GetAccount(ctx, addrKeysSlice[0].Address)
	acc1 = mapp.AccountKeeper.GetAccount(ctx, addrKeysSlice[1].Address)
	expectCoins0 = sdk.DecCoins{
		sdk.NewDecCoinFromDec(common.NativeToken, sdk.MustNewDecFromStr("90")),    // 100 - 10
		sdk.NewDecCoinFromDec(common.TestToken, sdk.MustNewDecFromStr("100.999")), // 100 + 1 * (1 - 0.001)
	}
	expectCoins1 = sdk.DecCoins{
		sdk.NewDecCoinFromDec(common.NativeToken, sdk.MustNewDecFromStr("109.7308")), // 100 + 10 * (1 - 0.001) - 0.2592
		sdk.NewDecCoinFromDec(common.TestToken, sdk.MustNewDecFromStr("97")),         // 100 - 0.5 - 2.5
	}
	require.EqualValues(t, expectCoins0.String(), acc0.GetCoins().String())
	require.EqualValues(t, expectCoins1.String(), acc1.GetCoins().String())
}

func TestEndBlockerDropExpireData(t *testing.T) {
	mapp, addrKeysSlice := getMockApp(t, 2)
	k := mapp.orderKeeper
	mapp.BeginBlock(abci.RequestBeginBlock{Header: abci.Header{Height: 2}})
	ctx := mapp.BaseApp.NewContext(false, abci.Header{}).WithBlockHeight(10)
	mapp.supplyKeeper.SetSupply(ctx, supply.NewSupply(mapp.TotalCoinsSupply))
	feeParams := types.DefaultParams()
	mapp.orderKeeper.SetParams(ctx, &feeParams)

	tokenPair := dex.GetBuiltInTokenPair()
	err := mapp.dexKeeper.SaveTokenPair(ctx, tokenPair)
	require.Nil(t, err)

	// mock orders
	orders := []*types.Order{
		types.MockOrder("", types.TestTokenPair, types.BuyOrder, "9.8", "1.0"),
		types.MockOrder("", types.TestTokenPair, types.SellOrder, "10.0", "1.5"),
		types.MockOrder("", types.TestTokenPair, types.BuyOrder, "10.0", "1.0"),
	}
	orders[0].Sender = addrKeysSlice[0].Address
	orders[1].Sender = addrKeysSlice[1].Address
	orders[2].Sender = addrKeysSlice[0].Address
	for i := 0; i < 3; i++ {
		err := k.PlaceOrder(ctx, orders[i])
		require.NoError(t, err)
	}

	EndBlocker(ctx, k) // update blockMatchResult, updatedOrderIds

	// check before expire: order, blockOrderNum, blockMatchResult, updatedOrderIDs
	require.NotNil(t, k.GetOrder(ctx, orders[1].OrderID))
	require.EqualValues(t, 3, k.GetBlockOrderNum(ctx, 10))
	blockMatchResult := k.GetBlockMatchResult()
	require.NotNil(t, blockMatchResult)
	updatedOrderIDs := k.GetUpdatedOrderIDs()
	require.EqualValues(t, []string{orders[2].OrderID, orders[1].OrderID}, updatedOrderIDs)

	ctx = mapp.BaseApp.NewContext(false, abci.Header{}).WithBlockHeight(11)
	EndBlocker(ctx, k)
	// call EndBlocker to expire orders
	ctx = mapp.BaseApp.NewContext(false, abci.Header{}).WithBlockHeight(10 + feeParams.OrderExpireBlocks)
	param := types.DefaultParams()
	mapp.orderKeeper.SetParams(ctx, &param)
	EndBlocker(ctx, k)

	order0 := k.GetOrder(ctx, orders[0].OrderID)
	order1 := k.GetOrder(ctx, orders[1].OrderID)
	order2 := k.GetOrder(ctx, orders[2].OrderID)

	require.EqualValues(t, types.OrderStatusExpired, order0.Status)
	require.EqualValues(t, types.OrderStatusPartialFilledExpired, order1.Status)
	require.Nil(t, order2)

	// call EndBlocker to drop expire orders
	ctx = mapp.BaseApp.NewContext(false, abci.Header{}).WithBlockHeight(11 + feeParams.OrderExpireBlocks)
	EndBlocker(ctx, k)

	// check after expire: order, blockOrderNum, blockMatchResult, updatedOrderIDs
	require.Nil(t, k.GetOrder(ctx, orders[0].OrderID))
	require.Nil(t, k.GetOrder(ctx, orders[1].OrderID))
	require.EqualValues(t, 0, k.GetBlockOrderNum(ctx, 10))
}

// test order expire when product is busy
func TestEndBlockerExpireOrdersBusyProduct(t *testing.T) {
	mapp, addrKeysSlice := getMockApp(t, 1)
	k := mapp.orderKeeper
	mapp.BeginBlock(abci.RequestBeginBlock{Header: abci.Header{Height: 2}})
	ctx := mapp.BaseApp.NewContext(false, abci.Header{}).WithBlockHeight(10)
	mapp.supplyKeeper.SetSupply(ctx, supply.NewSupply(mapp.TotalCoinsSupply))
	feeParams := types.DefaultParams()

	tokenPair := dex.GetBuiltInTokenPair()
	err := mapp.dexKeeper.SaveTokenPair(ctx, tokenPair)
	require.Nil(t, err)
	mapp.orderKeeper.SetParams(ctx, &feeParams)

	// mock orders
	orders := []*types.Order{
		types.MockOrder("", types.TestTokenPair, types.SellOrder, "10.0", "2.0"),
	}
	orders[0].Sender = addrKeysSlice[0].Address
	err = k.PlaceOrder(ctx, orders[0])
	require.NoError(t, err)
	EndBlocker(ctx, k)
	// call EndBlocker at 86400 + 9
	ctx = mapp.BaseApp.NewContext(false, abci.Header{}).
		WithBlockHeight(9 + feeParams.OrderExpireBlocks)
	EndBlocker(ctx, k)

	// call EndBlocker at 86400 + 10, lock product
	ctx = mapp.BaseApp.NewContext(false, abci.Header{}).
		WithBlockHeight(10 + feeParams.OrderExpireBlocks)
	lock := &types.ProductLock{
		Price:        sdk.MustNewDecFromStr("10.0"),
		Quantity:     sdk.MustNewDecFromStr("1.0"),
		BuyExecuted:  sdk.MustNewDecFromStr("1.0"),
		SellExecuted: sdk.MustNewDecFromStr("1.0"),
	}
	k.SetProductLock(ctx, types.TestTokenPair, lock)
	EndBlocker(ctx, k)

	// check order
	order := k.GetOrder(ctx, orders[0].OrderID)
	require.EqualValues(t, types.OrderStatusOpen, order.Status)
	require.EqualValues(t, 9+feeParams.OrderExpireBlocks, k.GetLastExpiredBlockHeight(ctx))

	// call EndBlocker at 86400 + 11, unlock product
	ctx = mapp.BaseApp.NewContext(false, abci.Header{}).
		WithBlockHeight(11 + feeParams.OrderExpireBlocks)
	k.UnlockProduct(ctx, types.TestTokenPair)
	EndBlocker(ctx, k)

	// check order
	order = k.GetOrder(ctx, orders[0].OrderID)
	require.EqualValues(t, types.OrderStatusExpired, order.Status)
	require.EqualValues(t, 11+feeParams.OrderExpireBlocks, k.GetLastExpiredBlockHeight(ctx))
}

func TestEndBlockerExpireOrders(t *testing.T) {
	mapp, addrKeysSlice := getMockApp(t, 3)
	k := mapp.orderKeeper
	mapp.BeginBlock(abci.RequestBeginBlock{Header: abci.Header{Height: 2}})

	var startHeight int64 = 10
	ctx := mapp.BaseApp.NewContext(false, abci.Header{}).WithBlockHeight(startHeight)
	mapp.supplyKeeper.SetSupply(ctx, supply.NewSupply(mapp.TotalCoinsSupply))

	feeParams := types.DefaultParams()

	tokenPair := dex.GetBuiltInTokenPair()
	err := mapp.dexKeeper.SaveTokenPair(ctx, tokenPair)
	require.Nil(t, err)

	tokenPairDex := dex.GetBuiltInTokenPair()
	err = mapp.dexKeeper.SaveTokenPair(ctx, tokenPairDex)
	require.Nil(t, err)

	mapp.orderKeeper.SetParams(ctx, &feeParams)
	EndBlocker(ctx, k)

	// mock orders
	orders := []*types.Order{
		types.MockOrder(types.FormatOrderID(startHeight, 1), types.TestTokenPair, types.BuyOrder, "9.8", "1.0"),
		types.MockOrder(types.FormatOrderID(startHeight, 2), types.TestTokenPair, types.SellOrder, "10.0", "1.0"),
		types.MockOrder(types.FormatOrderID(startHeight, 3), types.TestTokenPair, types.BuyOrder, "10.0", "0.5"),
	}
	orders[0].Sender = addrKeysSlice[0].Address
	orders[1].Sender = addrKeysSlice[1].Address
	orders[2].Sender = addrKeysSlice[2].Address
	for i := 0; i < 3; i++ {
		err := k.PlaceOrder(ctx, orders[i])
		require.NoError(t, err)
	}
	EndBlocker(ctx, k)

	// check account balance
	acc0 := mapp.AccountKeeper.GetAccount(ctx, addrKeysSlice[0].Address)
	acc1 := mapp.AccountKeeper.GetAccount(ctx, addrKeysSlice[1].Address)
	expectCoins0 := sdk.DecCoins{
		// 100 - 9.8 - 0.2592 = 89.9408
		sdk.NewDecCoinFromDec(common.NativeToken, sdk.MustNewDecFromStr("89.9408")),
		sdk.NewDecCoinFromDec(common.TestToken, sdk.MustNewDecFromStr("100")),
	}
	expectCoins1 := sdk.DecCoins{
		// 100 + 10 * 0.5 * (1 - 0.001) - 0.2592 = 104.7408
		sdk.NewDecCoinFromDec(common.NativeToken, sdk.MustNewDecFromStr("104.7358")),
		sdk.NewDecCoinFromDec(common.TestToken, sdk.MustNewDecFromStr("99")),
	}
	require.EqualValues(t, expectCoins0.String(), acc0.GetCoins().String())
	require.EqualValues(t, expectCoins1.String(), acc1.GetCoins().String())

	// check depth book
	depthBook := k.GetDepthBookCopy(types.TestTokenPair)
	require.EqualValues(t, 2, len(depthBook.Items))

	// call EndBlocker to expire orders
	mapp.BeginBlock(abci.RequestBeginBlock{Header: abci.Header{Height: 2}})
	ctx = mapp.BaseApp.NewContext(false, abci.Header{}).
		WithBlockHeight(startHeight + feeParams.OrderExpireBlocks)

	EndBlocker(ctx, k)

	// check order status
	order0 := k.GetOrder(ctx, orders[0].OrderID)
	order1 := k.GetOrder(ctx, orders[1].OrderID)
	require.EqualValues(t, types.OrderStatusExpired, order0.Status)
	require.EqualValues(t, types.OrderStatusPartialFilledExpired, order1.Status)

	// check depth book
	depthBook = k.GetDepthBookCopy(types.TestTokenPair)
	require.EqualValues(t, 0, len(depthBook.Items))
	// check order ids
	key := types.FormatOrderIDsKey(types.TestTokenPair, sdk.MustNewDecFromStr("9.8"), types.BuyOrder)
	orderIDs := k.GetProductPriceOrderIDs(key)
	require.EqualValues(t, 0, len(orderIDs))
	// check updated order ids
	updatedOrderIDs := k.GetUpdatedOrderIDs()
	require.EqualValues(t, 2, len(updatedOrderIDs))
	require.EqualValues(t, orders[0].OrderID, updatedOrderIDs[0])
	// check closed order id
	closedOrderIDs := k.GetDiskCache().GetClosedOrderIDs()
	require.Equal(t, 2, len(closedOrderIDs))
	require.Equal(t, orders[0].OrderID, closedOrderIDs[0])

	// check account balance
	acc0 = mapp.AccountKeeper.GetAccount(ctx, addrKeysSlice[0].Address)
	acc1 = mapp.AccountKeeper.GetAccount(ctx, addrKeysSlice[1].Address)
	expectCoins0 = sdk.DecCoins{
		sdk.NewDecCoinFromDec(common.NativeToken, sdk.MustNewDecFromStr("99.7408")), // 100 - 0.2592
		sdk.NewDecCoinFromDec(common.TestToken, sdk.MustNewDecFromStr("100")),
	}
	expectCoins1 = sdk.DecCoins{
		// 100 + 10 * 0.5 * (1 - 0.001) - 0.2592
		sdk.NewDecCoinFromDec(common.NativeToken, sdk.MustNewDecFromStr("104.7358")),
		sdk.NewDecCoinFromDec(common.TestToken, sdk.MustNewDecFromStr("99.5")),
	}
	require.EqualValues(t, expectCoins0.String(), acc0.GetCoins().String())
	require.EqualValues(t, expectCoins1.String(), acc1.GetCoins().String())

	// check fee pool
	feeCollector := mapp.supplyKeeper.GetModuleAccount(ctx, auth.FeeCollectorName)
	collectedFees := feeCollector.GetCoins()
	// 0.2592 + 0.2592
	require.EqualValues(t, "0.51840000"+common.NativeToken, collectedFees.String())
}

func TestEndBlockerCleanupOrdersWhoseTokenPairHaveBeenDelisted(t *testing.T) {
	mapp, addrKeysSlice := getMockApp(t, 2)
	k := mapp.orderKeeper
	mapp.BeginBlock(abci.RequestBeginBlock{Header: abci.Header{Height: 2}})

	var startHeight int64 = 10
	ctx := mapp.BaseApp.NewContext(false, abci.Header{}).WithBlockHeight(startHeight)
	mapp.supplyKeeper.SetSupply(ctx, supply.NewSupply(mapp.TotalCoinsSupply))

	feeParams := types.DefaultParams()
	mapp.orderKeeper.SetParams(ctx, &feeParams)

	// mock orders
	orders := []*types.Order{
		types.MockOrder(types.FormatOrderID(startHeight, 1), types.TestTokenPair, types.BuyOrder, "10.0", "1.0"),
		types.MockOrder(types.FormatOrderID(startHeight, 2), types.TestTokenPair, types.SellOrder, "10.0", "0.5"),
		types.MockOrder(types.FormatOrderID(startHeight, 3), types.TestTokenPair, types.SellOrder, "10.0", "2.5"),
	}
	orders[0].Sender = addrKeysSlice[0].Address
	orders[1].Sender = addrKeysSlice[1].Address
	orders[2].Sender = addrKeysSlice[1].Address
	for i := 0; i < 3; i++ {
		err := k.PlaceOrder(ctx, orders[i])
		require.NoError(t, err)
	}

	// call EndBlocker to execute periodic match
	EndBlocker(ctx, k)

	// check depth book
	depthBook := k.GetDepthBookCopy(types.TestTokenPair)
	require.EqualValues(t, 0, len(depthBook.Items))

	depthBookDB := k.GetDepthBookFromDB(ctx, types.TestTokenPair)
	require.EqualValues(t, 0, len(depthBookDB.Items))

	// check product price - order ids
	key := types.FormatOrderIDsKey(types.TestTokenPair, sdk.MustNewDecFromStr("10.0"), types.SellOrder)
	orderIDs := k.GetProductPriceOrderIDs(key)
	require.EqualValues(t, 0, len(orderIDs))

	key = types.FormatOrderIDsKey(types.TestTokenPair, sdk.MustNewDecFromStr("10.0"), types.BuyOrder)
	orderIDs = k.GetProductPriceOrderIDs(key)
	require.EqualValues(t, 0, len(orderIDs))

	// check closed order id
	closedOrderIDs := k.GetLastClosedOrderIDs(ctx)
	require.Equal(t, 3, len(closedOrderIDs))
	require.Equal(t, orders[0].OrderID, closedOrderIDs[0])
	require.Equal(t, orders[1].OrderID, closedOrderIDs[1])
	require.Equal(t, orders[2].OrderID, closedOrderIDs[2])

	// check account balance
	acc0 := mapp.AccountKeeper.GetAccount(ctx, addrKeysSlice[0].Address)
	acc1 := mapp.AccountKeeper.GetAccount(ctx, addrKeysSlice[1].Address)
	expectCoins0 := sdk.DecCoins{
		sdk.NewDecCoinFromDec(common.NativeToken, sdk.MustNewDecFromStr("100")),
		sdk.NewDecCoinFromDec(common.TestToken, sdk.MustNewDecFromStr("100")),
	}
	expectCoins1 := sdk.DecCoins{
		sdk.NewDecCoinFromDec(common.NativeToken, sdk.MustNewDecFromStr("100")),
		sdk.NewDecCoinFromDec(common.TestToken, sdk.MustNewDecFromStr("100")),
	}
	require.EqualValues(t, expectCoins0.String(), acc0.GetCoins().String())
	require.EqualValues(t, expectCoins1.String(), acc1.GetCoins().String())

	// check fee pool
	feeCollector := mapp.supplyKeeper.GetModuleAccount(ctx, auth.FeeCollectorName)
	collectedFees := feeCollector.GetCoins()
	require.EqualValues(t, "", collectedFees.String())
}